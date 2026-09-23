package aisettings

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/ai"
	"github.com/inroad/inroad/internal/platform/audit"
)

type captureRecorder struct{ events []audit.Event }

func (c *captureRecorder) Record(_ context.Context, ev audit.Event) error {
	c.events = append(c.events, ev)
	return nil
}

func TestSettingsChangesAreAuditedWithoutSecrets(t *testing.T) {
	rec := &captureRecorder{}
	svc := newTestService(t, newFakeStore(), svcOpts{})
	svc.audit = rec
	ws := uuid.New()

	p := mustCreateProvider(t, svc, ws, ProviderCreateInput{Kind: ai.KindAnthropic, DisplayName: "Main", Credentials: ai.Credentials{APIKey: testKey}})
	newKey := "sk-test-replacement-000000"
	if _, err := svc.UpdateProvider(context.Background(), ws, uuid.MustParse(p.ID), ProviderUpdateInput{Credentials: &ai.Credentials{APIKey: newKey}}); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	instructions := "Always sign off as the CEO"
	if _, err := svc.UpdateSettings(context.Background(), ws, SettingsUpdate{AdditionalInstructions: &instructions}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if err := svc.DeleteProvider(context.Background(), ws, uuid.MustParse(p.ID)); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}

	if len(rec.events) != 4 {
		t.Fatalf("recorded %d events, want 4", len(rec.events))
	}
	wantChange := []string{"created", "updated", "updated", "deleted"}
	for i, ev := range rec.events {
		if ev.Action != audit.ActionSettingsChanged || ev.WorkspaceID != ws || ev.Metadata["change"] != wantChange[i] {
			t.Fatalf("event %d = %+v", i, ev)
		}
		if err := audit.Validate(ev); err != nil {
			t.Fatalf("event %d would be refused at write: %v", i, err)
		}
		for k, v := range ev.Metadata {
			if strings.Contains(v, testKey) || strings.Contains(v, newKey) || strings.Contains(v, instructions) {
				t.Fatalf("event %d metadata %q leaks a key or prompt content", i, k)
			}
		}
	}
	if rec.events[1].Metadata["key_replaced"] != "true" {
		t.Fatalf("provider update did not record the key replacement: %v", rec.events[1].Metadata)
	}
	if rec.events[2].Metadata["area"] != "ai_settings" || rec.events[2].Metadata["fields"] != "additional_instructions" {
		t.Fatalf("settings event = %v", rec.events[2].Metadata)
	}
}
