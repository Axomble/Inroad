package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestContactsImportRequiresBulkRowsAndApprovalTier(t *testing.T) {
	importer := &fakeContactImports{result: ContactImportResult{Imported: 51}}
	tool := contactsImportTool(importer)
	if tool.Risk != RiskConsequential {
		t.Fatalf("risk=%s, want consequential", tool.Risk)
	}

	rows := make([]ContactInput, 51)
	for i := range rows {
		rows[i] = ContactInput{Email: fmt.Sprintf("person%d@example.com", i)}
	}
	args, err := json.Marshal(map[string]any{
		"loading_message": "Importing reviewed contacts",
		"list_id":         "22222222-2222-2222-2222-222222222222",
		"contacts":        rows,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), member(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || len(importer.gotRows) != 51 || importer.gotWS != wsID {
		t.Fatalf("result=%+v rows=%d workspace=%s", result, len(importer.gotRows), importer.gotWS)
	}
}

func TestContactsImportRejectsSmallAndInvalidBatchesBeforeMutation(t *testing.T) {
	importer := &fakeContactImports{}
	tool := contactsImportTool(importer)

	for name, contacts := range map[string][]ContactInput{
		"small":   {{Email: "person@example.com"}},
		"invalid": append(makeValidContacts(50), ContactInput{Email: "not-an-email"}),
	} {
		t.Run(name, func(t *testing.T) {
			args, err := json.Marshal(map[string]any{
				"loading_message": "Importing contacts",
				"list_id":         "22222222-2222-2222-2222-222222222222",
				"contacts":        contacts,
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := tool.Execute(context.Background(), member(), args)
			if err != nil {
				t.Fatal(err)
			}
			if result.Success || len(importer.gotRows) != 0 {
				t.Fatalf("result=%+v rows=%d", result, len(importer.gotRows))
			}
		})
	}
}

func makeValidContacts(count int) []ContactInput {
	rows := make([]ContactInput, count)
	for i := range rows {
		rows[i] = ContactInput{Email: fmt.Sprintf("person%d@example.com", i)}
	}
	return rows
}
