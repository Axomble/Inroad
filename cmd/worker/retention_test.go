package main

import (
	"strings"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/worker/maintenance"
)

const day = 24 * time.Hour

// The composition root is the ONLY producer of worker.Deps.Retention, whose zero
// value disables every table including dead letters. So this pins that the real
// mapping from configuration does not silently produce that zero value: with
// configuration at its defaults, dead letters keep their 90 days and every
// recipient-identifying table stays off.
func TestRetentionPolicyFromDefaultConfigKeepsDeadLetterRetention(t *testing.T) {
	p, err := retentionPolicy(&config.Config{RetentionDeadLettersDays: config.DefaultRetentionDeadLettersDays})
	if err != nil {
		t.Fatalf("retentionPolicy: %v", err)
	}
	want := maintenance.RetentionPolicy{DeadLetters: 90 * day}
	if p != want {
		t.Fatalf("policy = %+v, want %+v", p, want)
	}
}

// Each variable feeds its own table. A transposed pair here would pass every
// config test and apply, say, the sends window to deliverability events.
func TestRetentionPolicyMapsEachSettingToItsTable(t *testing.T) {
	p, err := retentionPolicy(&config.Config{
		RetentionDeliverabilityEventsDays: 100, RetentionInboxDays: 31, RetentionTrackingEventsDays: 32,
		RetentionSendsDays: 101, RetentionDeadLettersDays: 8,
	})
	if err != nil {
		t.Fatalf("retentionPolicy: %v", err)
	}
	want := maintenance.RetentionPolicy{
		DeliverabilityEvents: 100 * day, InboxThreads: 31 * day, TrackingEvents: 32 * day,
		Sends: 101 * day, DeadLetters: 8 * day,
	}
	if p != want {
		t.Fatalf("policy = %+v, want %+v", p, want)
	}
}

// Below a floor, the worker must refuse to start — and say which setting.
func TestRetentionPolicyRefusesAWindowBelowItsFloor(t *testing.T) {
	_, err := retentionPolicy(&config.Config{RetentionSendsDays: 30})
	if err == nil {
		t.Fatal("a 30-day sends window was accepted; the floor is 90")
	}
	if !strings.Contains(err.Error(), "INROAD_RETENTION_SENDS_DAYS") {
		t.Fatalf("error %q does not name the setting", err)
	}
}
