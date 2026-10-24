package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePhone struct {
	verified bool
	sent     int
}

func (f *fakePhone) sendPhoneCode(context.Context, string, string) error { return nil }
func (f *fakePhone) verifyPhone(context.Context, string, string, string) (json.RawMessage, error) {
	if !f.verified {
		return nil, rejected{Status: 400, Code: "DENIED", Message: "invalid code"}
	}
	return json.RawMessage(`{}`), nil
}
func (f *fakePhone) sendInspectionCode(context.Context, string, string, string) error {
	f.sent++
	return nil
}

func TestInspectionDecision(t *testing.T) {
	for _, tc := range []struct {
		name      string
		verified  bool
		wantLogin bool
		wantSMS   int
	}{
		{"unverified tenant", false, false, 0},
		{"verified tenant", true, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakePhone{verified: tc.verified}
			s := newPropertyService(fake)
			token, err := s.login(context.Background(), "+15551234567", "123456", "login-1")
			if (err == nil) != tc.wantLogin {
				t.Fatalf("login error = %v", err)
			}
			if err == nil {
				s.reminders["inspection-1"] = inspectionReminder{ID: "inspection-1", Phone: "+15551234567", Unit: "4B", Date: "2026-10-01", State: "scheduled", code: "1234"}
				if _, err := s.remind(context.Background(), s.owner(token), "inspection-1"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.acknowledge(s.owner(token), "inspection-1", "0000"); err == nil {
					t.Fatal("incorrect confirmation accepted")
				}
				result, err := s.acknowledge(s.owner(token), "inspection-1", "1234")
				if err != nil || result.State != "acknowledged" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			}
			if fake.sent != tc.wantSMS {
				t.Fatalf("SMS calls = %d, want %d", fake.sent, tc.wantSMS)
			}
		})
	}
}

func TestInfraiEnvelopeAndSharedCredential(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Idempotency-Key") == "" {
			t.Errorf("request headers: %+v", r.Header)
		}
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/verify") {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"DENIED","message":"invalid code"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{},"metadata":{}}`))
	}))
	defer server.Close()
	c := infraiClient{base: server.URL, key: "test-key", http: server.Client()}
	if err := c.sendPhoneCode(context.Background(), "+15551234567", "send-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.sendInspectionCode(context.Background(), "+15551234567", "1234", "sms-1"); err != nil {
		t.Fatal(err)
	}
	_, err := c.verifyPhone(context.Background(), "+15551234567", "0000", "verify-1")
	var denied rejected
	if !errorsAs(err, &denied) || denied.Status != 400 || denied.Code != "DENIED" {
		t.Fatalf("envelope rejection = %v", err)
	}
	if len(paths) != 3 || paths[0] != "/v1/auth/phone/send_code" || paths[1] != "/v1/sms/otp" || paths[2] != "/v1/auth/phone/verify" {
		t.Fatalf("paths = %v", paths)
	}
}

func errorsAs(err error, out *rejected) bool {
	v, ok := err.(rejected)
	if ok {
		*out = v
	}
	return ok
}
