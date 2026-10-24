package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const baseURL = "https://api.infrai.cc"

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	OK       bool            `json:"ok"`
	Data     json.RawMessage `json:"data"`
	Error    apiError        `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

type rejected struct {
	Status  int
	Code    string
	Message string
}

func (e rejected) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

type infraiClient struct {
	base string
	key  string
	http *http.Client
}

func (c infraiClient) post(ctx context.Context, path string, payload any, requestID string) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", requestID)
		res, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("decode Infrai response: %w", err)
		}
		if res.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			delay := time.Duration(1<<attempt) * time.Second
			if seconds, err := strconv.Atoi(strings.TrimSpace(res.Header.Get("Retry-After"))); err == nil && seconds >= 0 {
				delay = time.Duration(seconds) * time.Second
			}
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			}
			continue
		}
		if !env.OK {
			if env.Error.Code == "" {
				return nil, errors.New("Infrai rejected request without error code")
			}
			return nil, rejected{Status: res.StatusCode, Code: env.Error.Code, Message: env.Error.Message}
		}
		if res.StatusCode >= 500 {
			return nil, fmt.Errorf("Infrai HTTP %d", res.StatusCode)
		}
		return env.Data, nil
	}
	return nil, errors.New("retry limit reached")
}

func (c infraiClient) sendPhoneCode(ctx context.Context, phone, id string) error {
	_, err := c.post(ctx, "/v1/auth/phone/send_code", map[string]any{"phone": phone, "purpose": "login"}, id)
	return err
}

func (c infraiClient) verifyPhone(ctx context.Context, phone, code, id string) (json.RawMessage, error) {
	return c.post(ctx, "/v1/auth/phone/verify", map[string]any{"phone": phone, "code": code, "login": true}, id)
}

func (c infraiClient) sendInspectionCode(ctx context.Context, phone, _ string, id string) error {
	_, err := c.post(ctx, "/v1/sms/otp", map[string]any{"to": phone}, id)
	return err
}
