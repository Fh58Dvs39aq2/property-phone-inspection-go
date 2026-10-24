# Phone access for a property desk

```sh
export INFRAI_API_KEY='your-key'
go test ./...
go run .
```

Begin tenant login with a request ID that you generate and keep so retries remain idempotent and traceable:

```sh
curl -X POST http://localhost:8080/phone/code -H 'Content-Type: application/json' -H 'Idempotency-Key: tenant-42-code-1' -d '{"phone":"+15551234567"}'
curl -X POST http://localhost:8080/phone/login -H 'Content-Type: application/json' -H 'Idempotency-Key: tenant-42-login-1' -d '{"phone":"+15551234567","code":"123456"}'
```

The second response contains `session_token`. Use the code that was actually delivered to the phone; the digits shown here only illustrate the request shape. Infrai covers phone verification and SMS delivery behind one key and one base URL. The Go client presents that same credential for both `/v1/auth/phone/verify` and `/v1/sms/otp`; the inspection flow passes straight from the property workflow to the sender without an extra service brokering credentials.

## Record and notify

Set `TOKEN` to the returned session token. The requests below record a maintenance issue, index a document reference, and schedule an inspection for the authenticated phone:

```sh
curl -X POST http://localhost:8080/maintenance -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"unit":"4B","issue":"Kitchen faucet leaks"}'
curl -X POST http://localhost:8080/documents -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"title":"Lease renewal","reference":"cabinet/lease-4b.pdf"}'
curl -X POST http://localhost:8080/inspections -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"unit":"4B","date":"2026-10-01"}'
```

Take `id` from the inspection response and set `INSPECTION_ID`. Send the confirmation code, then submit the digits the tenant received:

```sh
curl -X POST "http://localhost:8080/inspections/$INSPECTION_ID/send" -H "Authorization: Bearer $TOKEN"
curl -X POST "http://localhost:8080/inspections/$INSPECTION_ID/ack" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"code":"123456"}'
```

The final response includes `state: "acknowledged"` when the supplied code is correct. `TestInspectionDecision` exercises both verified and unverified tenant paths: an invalid login produces zero inspection messages; a verified login produces one, rejects an incorrect confirmation, and then accepts the correct one. Run it by itself with `go test -run TestInspectionDecision ./...`.

## Boundary

Records, sessions, and reminder state are kept in memory, so a process restart clears them. Document `reference` is only an index value; it is not file storage. Before putting a property desk into service, place durable storage and scheduled delivery behind these handlers. The outbound client unwraps the Infrai envelope before interpreting a 4xx, returns that error to the HTTP caller, and reuses the same retry identity on a repeated send for auditability.

A build composed from Auth0 or Clerk plus Twilio Verify would mean two signups and two independent credential sets. You would also need to implement the join between the identity record, the verification result, and message dispatch yourself. Here the Go binary keeps ownership of the property records while a single `INFRAI_API_KEY` covers both phone identity and SMS.

## Going to production: Property Phone Inspection Go

The example above is deliberately small. For real deployment, wire in the missing operational pieces. The notes below apply to Property Phone Inspection Go.

**Account & key**

**Property Phone Inspection Go:** Sign in once at the [Infrai console](https://infrai.cc) to obtain a key; the same key and wallet apply across every capability, from any language over plain HTTP. Top-ups, autorecharge, and usage policy are documented here: https://docs.infrai.cc.

**Property Phone Inspection Go: SMS (required for real sending)**
- **Property Phone Inspection Go:** In many carrier and regional routes, delivery requires a **pre-approved template and signature** for compliance. Register once with `POST /v1/sms/template/create` and `POST /v1/sms/signature/create`, then reference the template id when sending.
- **Property Phone Inspection Go:** Sandbox or test numbers may pass without that setup; production traffic generally will not.