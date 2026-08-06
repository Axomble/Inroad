package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

// minContactQuery mirrors contact.MinQueryLen: below it the trigram index
// stops being selective and the domain rejects the search, so the tool says so
// up front instead of surfacing a validation error the model cannot parse.
const minContactQuery = 2

// ContactQuery is one page of the contact domain's indexed search. An empty
// Query means "no text filter" (a plain listing); a nil ListID means the whole
// workspace.
type ContactQuery struct {
	Query  string
	ListID *uuid.UUID
	Limit  int
}

// ContactMatch is one contact as a tool reports it.
type ContactMatch struct {
	ID        uuid.UUID
	Email     string
	FirstName string
	CreatedAt time.Time
}

// ContactPage is a page of matches plus the capped total the domain reports.
type ContactPage struct {
	Matches []ContactMatch
	Total   int64
	// TotalIsCapped means Total is a floor ("at least this many"), not exact.
	TotalIsCapped bool
}

// ContactReader is the read half of the contact domain as these tools need it.
// An unknown or cross-workspace ListID must come back as pgx.ErrNoRows so the
// tool can tell the model to pick a real list rather than aborting the run.
type ContactReader interface {
	Search(ctx context.Context, ws uuid.UUID, q ContactQuery) (ContactPage, error)
}

// ContactInput is a new contact's fields.
type ContactInput struct{ Email, FirstName, LastName, Company string }

// ContactWriter is the write half. Implementations must resolve every id
// inside ws — the tools never pass a workspace the model chose.
type ContactWriter interface {
	// Create returns the contact id and whether the row was newly inserted; an
	// email already present in the workspace comes back created=false.
	Create(ctx context.Context, ws uuid.UUID, in ContactInput) (id uuid.UUID, created bool, err error)
	// AddToList is idempotent. An unknown list or contact must return
	// pgx.ErrNoRows.
	AddToList(ctx context.Context, ws, listID, contactID uuid.UUID) error
}

type ContactImportResult struct {
	Imported   int
	Skipped    int
	Duplicates int
}

// ContactImporter is the bulk-write seam. The concrete adapter reuses the
// contact domain's validated CSV importer instead of bypassing its ownership
// and deduplication rules.
type ContactImporter interface {
	Import(context.Context, uuid.UUID, uuid.UUID, []ContactInput) (ContactImportResult, error)
}

type contactView struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"first_name,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

func contactTools(deps Deps) []Tool {
	var out []Tool
	if deps.Contacts != nil {
		out = append(out, contactReadTool(deps.Contacts))
	}
	if deps.ContactWrites != nil {
		out = append(out, contactWriteTool(deps.ContactWrites))
	}
	if deps.ContactImports != nil {
		out = append(out, contactsImportTool(deps.ContactImports))
	}
	return out
}

type contactReadArgs struct {
	baseArgs
	Method string `json:"method"`
	Query  string `json:"query"`
	ListID string `json:"list_id"`
	Limit  *int   `json:"limit"`
}

func contactReadTool(r ContactReader) Tool {
	methods := []string{methodList, methodSearch}
	return Tool{
		Name: "inroad_contact_read",
		Description: "Read contacts in this workspace. Use method=search with a name-or-email fragment to find specific people, " +
			"and method=list to browse, optionally narrowed to one contact list with list_id. " +
			fmt.Sprintf("Results are capped at %d by default (maximum %d) and the total is reported separately, so ask for a count rather than paging to find one. ", defaultLimit, maxLimit) +
			fmt.Sprintf("A search query must be at least %d characters.", minContactQuery),
		InputSchema: mustSchema(
			methodField("search filters by a text fragment; list browses without one.", methods),
			strField("query", fmt.Sprintf("Text to match against contact email and name; at least %d characters. Required for search.", minContactQuery), false),
			strField("list_id", "Restrict to one contact list, from inroad_list_read. Optional.", false),
			limitField(),
		),
		Risk: RiskRead,
		Execute: func(ctx context.Context, p Principal, args json.RawMessage) (Result, error) {
			var a contactReadArgs
			if bad := decodeArgs(args, &a); bad != nil {
				return *bad, nil
			}
			limit, bad := resolveLimit(a.Limit)
			if bad != nil {
				return *bad, nil
			}

			q := ContactQuery{Limit: limit}
			switch a.Method {
			case methodSearch:
				q.Query = strings.TrimSpace(a.Query)
				if len(q.Query) < minContactQuery {
					return Fail(fmt.Sprintf(
						"method=search needs a query of at least %d characters; use method=list to browse without a filter", minContactQuery)), nil
				}
			case methodList:
			default:
				return unknownMethod(a.Method, methods), nil
			}
			if a.ListID != "" {
				listID, bad := parseID("list_id", a.ListID)
				if bad != nil {
					return *bad, nil
				}
				q.ListID = &listID
			}

			page, err := r.Search(ctx, p.WorkspaceID, q)
			if err != nil {
				if isNoRecord(err) {
					return listMissing(a.ListID), nil
				}
				return Result{}, fmt.Errorf("search contacts: %w", err)
			}
			items := make([]contactView, 0, len(page.Matches))
			for _, m := range page.Matches {
				items = append(items, contactView{
					ID: m.ID.String(), Email: m.Email, FirstName: m.FirstName, CreatedAt: rfc3339Time(m.CreatedAt),
				})
			}
			return Ok(map[string]any{
				"contacts": items, "returned": len(items),
				"total": page.Total, "total_is_at_least": page.TotalIsCapped,
			}), nil
		},
	}
}

type contactWriteArgs struct {
	baseArgs
	Method    string `json:"method"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Company   string `json:"company"`
	ContactID string `json:"contact_id"`
	ListID    string `json:"list_id"`
}

func contactWriteTool(w ContactWriter) Tool {
	methods := []string{methodCreate, methodAddToList}
	return Tool{
		Name: "inroad_contact_write",
		Description: "Create a contact in this workspace, or add an existing contact to a contact list. " +
			"Use method=create with the person's email (names and company optional); an email that already exists is reported back rather than duplicated. " +
			"Use method=add_to_list with contact_id and list_id — adding a contact that is already a member is a no-op. " +
			"This writes data but sends nothing; enrolling a list in a campaign is a separate, approved action.",
		InputSchema: mustSchema(
			methodField("create adds a contact; add_to_list puts an existing contact on a list.", methods),
			strField("email", "The contact's email address. Required for create.", false),
			strField("first_name", "The contact's first name. Optional.", false),
			strField("last_name", "The contact's last name. Optional.", false),
			strField("company", "The contact's company name. Optional.", false),
			strField("contact_id", "The contact's id, from inroad_contact_read. Required for add_to_list.", false),
			strField("list_id", "The target list's id, from inroad_list_read. Required for add_to_list.", false),
		),
		Risk: RiskWrite,
		Execute: func(ctx context.Context, p Principal, args json.RawMessage) (Result, error) {
			var a contactWriteArgs
			if bad := decodeArgs(args, &a); bad != nil {
				return *bad, nil
			}
			switch a.Method {
			case methodCreate:
				return contactCreate(ctx, w, p.WorkspaceID, a)
			case methodAddToList:
				return contactAddToList(ctx, w, p.WorkspaceID, a)
			default:
				return unknownMethod(a.Method, methods), nil
			}
		},
	}
}

func contactCreate(ctx context.Context, w ContactWriter, ws uuid.UUID, a contactWriteArgs) (Result, error) {
	email := strings.TrimSpace(a.Email)
	if !isBareEmailAddress(email) {
		return Fail("email must be a single valid address like name@example.com; call again with a corrected email"), nil
	}
	id, created, err := w.Create(ctx, ws, ContactInput{
		Email:     email,
		FirstName: strings.TrimSpace(a.FirstName),
		LastName:  strings.TrimSpace(a.LastName),
		Company:   strings.TrimSpace(a.Company),
	})
	if err != nil {
		return Result{}, fmt.Errorf("create contact: %w", err)
	}
	return Ok(map[string]any{"id": id.String(), "email": email, "created": created}), nil
}

func isBareEmailAddress(value string) bool {
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Name == "" && parsed.Address == value
}

func contactAddToList(ctx context.Context, w ContactWriter, ws uuid.UUID, a contactWriteArgs) (Result, error) {
	contactID, bad := parseID("contact_id", a.ContactID)
	if bad != nil {
		return *bad, nil
	}
	listID, bad := parseID("list_id", a.ListID)
	if bad != nil {
		return *bad, nil
	}
	if err := w.AddToList(ctx, ws, listID, contactID); err != nil {
		if isNoRecord(err) {
			return Fail(fmt.Sprintf(
				"no contact %s or no list %s in this workspace; confirm both ids with inroad_contact_read and inroad_list_read before retrying",
				a.ContactID, a.ListID)), nil
		}
		return Result{}, fmt.Errorf("add contact to list: %w", err)
	}
	return Ok(map[string]any{"contact_id": contactID.String(), "list_id": listID.String(), "added": true}), nil
}

const maxAgentImportRows = 1000

type contactsImportArgs struct {
	baseArgs
	ListID   string         `json:"list_id"`
	Contacts []ContactInput `json:"contacts"`
}

func contactsImportTool(importer ContactImporter) Tool {
	contactItem := jsonObject{
		{"type", "object"},
		{"properties", jsonObject{
			{"email", jsonObject{{"type", "string"}, {"description", "A single valid email address."}}},
			{"first_name", jsonObject{{"type", "string"}}},
			{"last_name", jsonObject{{"type", "string"}}},
			{"company", jsonObject{{"type", "string"}}},
		}},
		{"required", []string{"email"}},
		{"additionalProperties", false},
	}
	contacts := field{name: "contacts", required: true, schema: jsonObject{
		{"type", "array"},
		{"description", fmt.Sprintf("Contacts to import. This approval-gated bulk tool accepts 51-%d rows; use inroad_contact_write for 50 or fewer.", maxAgentImportRows)},
		{"items", contactItem}, {"minItems", 51}, {"maxItems", maxAgentImportRows},
	}}
	return Tool{
		Name: "inroad_contacts_import",
		Description: "Import more than 50 contacts into an existing contact list in one consequential operation. " +
			"Every call pauses for human review, where the exact rows can be edited before execution. Use inroad_contact_write for 50 or fewer contacts.",
		InputSchema: mustSchema(
			strField("list_id", "The target contact list id, from inroad_list_read.", true),
			contacts,
		),
		Risk: RiskConsequential,
		Execute: func(ctx context.Context, p Principal, args json.RawMessage) (Result, error) {
			var a contactsImportArgs
			if bad := decodeArgs(args, &a); bad != nil {
				return *bad, nil
			}
			if len(a.Contacts) < 51 || len(a.Contacts) > maxAgentImportRows {
				return Fail(fmt.Sprintf("contacts must contain 51-%d rows; use inroad_contact_write for smaller imports", maxAgentImportRows)), nil
			}
			listID, bad := parseID("list_id", a.ListID)
			if bad != nil {
				return *bad, nil
			}
			for i := range a.Contacts {
				a.Contacts[i].Email = strings.TrimSpace(a.Contacts[i].Email)
				if !isBareEmailAddress(a.Contacts[i].Email) {
					return Fail(fmt.Sprintf("contacts[%d].email must be a single valid address like name@example.com", i)), nil
				}
			}
			result, err := importer.Import(ctx, p.WorkspaceID, listID, a.Contacts)
			if err != nil {
				if isNoRecord(err) {
					return listMissing(a.ListID), nil
				}
				return Result{}, fmt.Errorf("import contacts: %w", err)
			}
			return Ok(map[string]int{
				"imported": result.Imported, "skipped": result.Skipped, "duplicates": result.Duplicates,
			}), nil
		},
	}
}
