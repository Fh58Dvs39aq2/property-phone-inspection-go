package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, err error) {
	var denial rejected
	if errors.As(err, &denial) {
		status := denial.Status
		if status < 400 || status >= 500 {
			status = http.StatusBadRequest
		}
		respond(w, status, map[string]string{"error": denial.Error()})
		return
	}
	respond(w, http.StatusBadGateway, map[string]string{"error": "upstream request failed"})
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		respond(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return false
	}
	return true
}

func requestID(r *http.Request) string { return strings.TrimSpace(r.Header.Get("Idempotency-Key")) }

func routes(s *propertyService) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /phone/code", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Phone string `json:"phone"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Phone == "" || requestID(r) == "" {
			respond(w, 400, map[string]string{"error": "phone and Idempotency-Key required"})
			return
		}
		if err := s.api.sendPhoneCode(r.Context(), in.Phone, requestID(r)); err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, map[string]string{"state": "code_sent"})
	})
	mux.HandleFunc("POST /phone/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Phone string `json:"phone"`
			Code  string `json:"code"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Phone == "" || in.Code == "" || requestID(r) == "" {
			respond(w, 400, map[string]string{"error": "phone, code and Idempotency-Key required"})
			return
		}
		token, err := s.login(r.Context(), in.Phone, in.Code, requestID(r))
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, map[string]string{"session_token": token})
	})
	mux.HandleFunc("POST /maintenance", func(w http.ResponseWriter, r *http.Request) {
		phone := s.owner(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if phone == "" {
			respond(w, 401, map[string]string{"error": "login required"})
			return
		}
		var in struct {
			Unit  string `json:"unit"`
			Issue string `json:"issue"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Unit == "" || in.Issue == "" {
			respond(w, 400, map[string]string{"error": "unit and issue required"})
			return
		}
		id, err := newID()
		if err != nil {
			respond(w, 500, map[string]string{"error": "cannot allocate record"})
			return
		}
		item := maintenanceRequest{ID: id, Phone: phone, Unit: in.Unit, Issue: in.Issue, State: "open"}
		s.mu.Lock()
		s.requests[id] = item
		s.mu.Unlock()
		respond(w, 201, item)
	})
	mux.HandleFunc("POST /documents", func(w http.ResponseWriter, r *http.Request) {
		phone := s.owner(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if phone == "" {
			respond(w, 401, map[string]string{"error": "login required"})
			return
		}
		var in struct {
			Title     string `json:"title"`
			Reference string `json:"reference"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Title == "" || in.Reference == "" {
			respond(w, 400, map[string]string{"error": "title and reference required"})
			return
		}
		id, err := newID()
		if err != nil {
			respond(w, 500, map[string]string{"error": "cannot allocate record"})
			return
		}
		item := tenantDocument{ID: id, Phone: phone, Title: in.Title, Reference: in.Reference}
		s.mu.Lock()
		s.documents[id] = item
		s.mu.Unlock()
		respond(w, 201, item)
	})
	mux.HandleFunc("POST /inspections", func(w http.ResponseWriter, r *http.Request) {
		phone := s.owner(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if phone == "" {
			respond(w, 401, map[string]string{"error": "login required"})
			return
		}
		var in struct {
			Unit string `json:"unit"`
			Date string `json:"date"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Unit == "" {
			respond(w, 400, map[string]string{"error": "unit required"})
			return
		}
		if _, err := time.Parse("2006-01-02", in.Date); err != nil {
			respond(w, 400, map[string]string{"error": "date must be YYYY-MM-DD"})
			return
		}
		id, err := newID()
		if err != nil {
			respond(w, 500, map[string]string{"error": "cannot allocate record"})
			return
		}
		var digits [6]byte
		if _, err := rand.Read(digits[:]); err != nil {
			respond(w, 500, map[string]string{"error": "cannot allocate confirmation"})
			return
		}
		// The reminder ID is also the retry identity for its outbound SMS.
		for i := range digits {
			digits[i] = digits[i]%10 + '0'
		}
		item := inspectionReminder{ID: id, Phone: phone, Unit: in.Unit, Date: in.Date, State: "scheduled", code: string(digits[:])}
		s.mu.Lock()
		s.reminders[id] = item
		s.mu.Unlock()
		respond(w, 201, item)
	})
	mux.HandleFunc("POST /inspections/{id}/send", func(w http.ResponseWriter, r *http.Request) {
		phone := s.owner(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if phone == "" {
			respond(w, 401, map[string]string{"error": "login required"})
			return
		}
		item, err := s.remind(r.Context(), phone, r.PathValue("id"))
		if err != nil {
			var denial rejected
			if errors.As(err, &denial) {
				fail(w, err)
			} else {
				if err.Error() == "reminder not found" {
					respond(w, 404, map[string]string{"error": "reminder not found"})
				} else {
					fail(w, err)
				}
			}
			return
		}
		respond(w, 200, item)
	})
	mux.HandleFunc("POST /inspections/{id}/ack", func(w http.ResponseWriter, r *http.Request) {
		phone := s.owner(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if phone == "" {
			respond(w, 401, map[string]string{"error": "login required"})
			return
		}
		var in struct {
			Code string `json:"code"`
		}
		if !decode(w, r, &in) {
			return
		}
		item, err := s.acknowledge(phone, r.PathValue("id"), in.Code)
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, item)
	})
	return mux
}

func main() {
	key := os.Getenv("INFRAI_API_KEY")
	if key == "" {
		log.Fatal("set INFRAI_API_KEY")
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	client := infraiClient{base: baseURL, key: key, http: &http.Client{Timeout: 10 * time.Second}}
	log.Fatal(http.ListenAndServe(addr, routes(newPropertyService(client))))
}
