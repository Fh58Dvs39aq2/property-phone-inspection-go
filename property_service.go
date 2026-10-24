package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
)

type phoneGateway interface {
	sendPhoneCode(context.Context, string, string) error
	verifyPhone(context.Context, string, string, string) (json.RawMessage, error)
	sendInspectionCode(context.Context, string, string, string) error
}

type maintenanceRequest struct {
	ID    string `json:"id"`
	Phone string `json:"phone"`
	Unit  string `json:"unit"`
	Issue string `json:"issue"`
	State string `json:"state"`
}

type tenantDocument struct {
	ID        string `json:"id"`
	Phone     string `json:"phone"`
	Title     string `json:"title"`
	Reference string `json:"reference"`
}

type inspectionReminder struct {
	ID    string `json:"id"`
	Phone string `json:"phone"`
	Unit  string `json:"unit"`
	Date  string `json:"date"`
	State string `json:"state"`
	code  string
}

type propertyService struct {
	mu        sync.Mutex
	api       phoneGateway
	sessions  map[string]string
	requests  map[string]maintenanceRequest
	documents map[string]tenantDocument
	reminders map[string]inspectionReminder
}

func newPropertyService(api phoneGateway) *propertyService {
	return &propertyService{api: api, sessions: make(map[string]string), requests: make(map[string]maintenanceRequest), documents: make(map[string]tenantDocument), reminders: make(map[string]inspectionReminder)}
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *propertyService) login(ctx context.Context, phone, code, id string) (string, error) {
	if _, err := s.api.verifyPhone(ctx, phone, code, id); err != nil {
		return "", err
	}
	token, err := newID()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.sessions[token] = phone
	s.mu.Unlock()
	return token, nil
}

func (s *propertyService) owner(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[token]
}

func (s *propertyService) remind(ctx context.Context, phone, id string) (inspectionReminder, error) {
	s.mu.Lock()
	r, ok := s.reminders[id]
	s.mu.Unlock()
	if !ok || r.Phone != phone {
		return inspectionReminder{}, errors.New("reminder not found")
	}
	if r.State == "acknowledged" {
		return r, nil
	}
	if err := s.api.sendInspectionCode(ctx, r.Phone, r.code, r.ID); err != nil {
		return inspectionReminder{}, err
	}
	s.mu.Lock()
	r.State = "sent"
	s.reminders[id] = r
	s.mu.Unlock()
	return r, nil
}

func (s *propertyService) acknowledge(phone, id, code string) (inspectionReminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reminders[id]
	if !ok || r.Phone != phone {
		return inspectionReminder{}, errors.New("reminder not found")
	}
	if r.State != "sent" || subtle.ConstantTimeCompare([]byte(r.code), []byte(code)) != 1 {
		return inspectionReminder{}, errors.New("invalid confirmation")
	}
	r.State = "acknowledged"
	s.reminders[id] = r
	return r, nil
}
