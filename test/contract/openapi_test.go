package contract_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestCanonicalOpenAPIHasNoDuplicateObjectKeys(t *testing.T) {
	body, err := os.ReadFile(repositoryPath(t, "contract/openapi/v1/openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := rejectDuplicateKeys(decoder); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalOpenAPIIsParseableAndOwnsEveryHTTPRoute(t *testing.T) {
	document := loadOpenAPI(t)
	if document.OpenAPI != "3.1.0" || document.Info.Version != "3.0.0" {
		t.Fatalf("OpenAPI/version = %q/%q", document.OpenAPI, document.Info.Version)
	}
	wantPaths := []string{
		"/healthz",
		"/readyz",
		"/internal/scheduler/v1/schedules/{name}",
		"/internal/scheduler/v1/schedules/{name}/pause",
		"/internal/scheduler/v1/schedules/{name}/resume",
	}
	if len(document.Paths) != len(wantPaths) {
		t.Fatalf("path count = %d, want %d", len(document.Paths), len(wantPaths))
	}
	for _, path := range wantPaths {
		if _, found := document.Paths[path]; !found {
			t.Errorf("canonical OpenAPI is missing %s", path)
		}
	}
	for path := range document.Paths {
		if strings.Contains(path, "/jobs") {
			t.Errorf("legacy in-memory job route remains in canonical contract: %s", path)
		}
	}
}

func TestEveryOperationDeclaresOwnerVisibilityIdempotencyAndPermission(t *testing.T) {
	document := loadOpenAPI(t)
	operationIDs := make(map[string]string)
	for path, item := range document.Paths {
		for method, raw := range item {
			if method == "parameters" {
				continue
			}
			operation, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("%s %s operation is not an object", method, path)
			}
			for _, field := range []string{"operationId", "x-kokoro-owner", "x-kokoro-visibility", "x-kokoro-stability", "x-kokoro-idempotency", "x-kokoro-permission", "responses"} {
				if _, found := operation[field]; !found {
					t.Errorf("%s %s missing %s", method, path, field)
				}
			}
			if operation["x-kokoro-owner"] != "kokoro-scheduler" || operation["x-kokoro-visibility"] != "internal-owner" {
				t.Errorf("%s %s has invalid owner/visibility", method, path)
			}
			assertUniqueOperationID(t, operationIDs, operation, method+" "+path)
		}
	}
	for name, item := range document.Webhooks {
		for method, raw := range item {
			operation, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("webhook %s %s is not an object", name, method)
			}
			for _, field := range []string{"operationId", "x-kokoro-owner", "x-kokoro-visibility", "x-kokoro-stability", "x-kokoro-idempotency", "x-kokoro-permission", "responses"} {
				if _, found := operation[field]; !found {
					t.Errorf("webhook %s %s missing %s", name, method, field)
				}
			}
			if operation["x-kokoro-owner"] != "kokoro-scheduler" || operation["x-kokoro-visibility"] != "event-protocol" {
				t.Errorf("webhook %s %s has invalid owner/visibility", name, method)
			}
			assertUniqueOperationID(t, operationIDs, operation, method+" webhook "+name)
		}
	}
}

func assertUniqueOperationID(t *testing.T, seen map[string]string, operation map[string]any, location string) {
	t.Helper()
	id, ok := operation["operationId"].(string)
	if !ok || id == "" {
		t.Errorf("%s has an invalid operationId", location)
		return
	}
	if prior, exists := seen[id]; exists {
		t.Errorf("operationId %q is duplicated by %s and %s", id, prior, location)
		return
	}
	seen[id] = location
}

func TestScheduleContractRequiresTenantTimezoneRecoveryAndRetryFields(t *testing.T) {
	document := loadOpenAPI(t)
	parameters := object(t, document.Components, "parameters")
	tenant := object(t, parameters, "TenantId")
	if tenant["name"] != "X-Kokoro-Tenant-Id" || tenant["required"] != true {
		t.Fatalf("tenant parameter = %#v", tenant)
	}
	schemas := object(t, document.Components, "schemas")
	input := object(t, schemas, "ScheduleInput")
	properties := object(t, input, "properties")
	for _, field := range []string{"schedule", "timezone", "url", "method", "body", "retry", "misfire_policy", "catch_up_limit", "overlap_policy", "paused"} {
		if _, found := properties[field]; !found {
			t.Errorf("ScheduleInput is missing %s", field)
		}
	}
	retry := object(t, schemas, "RetryPolicy")
	retryProperties := object(t, retry, "properties")
	for _, field := range []string{"max_attempts", "backoff_seconds", "max_backoff_seconds", "max_retry_window_seconds"} {
		if _, found := retryProperties[field]; !found {
			t.Errorf("RetryPolicy is missing %s", field)
		}
	}
}

func TestScheduleResponseSchemaMatchesConcreteTransportShape(t *testing.T) {
	document := loadOpenAPI(t)
	schemas := object(t, document.Components, "schemas")
	schedule := object(t, schemas, "Schedule")
	if schedule["additionalProperties"] != false {
		t.Fatal("Schedule response must reject undeclared fields")
	}
	properties := object(t, schedule, "properties")
	want := []string{
		"id", "name", "schedule", "timezone", "url", "method", "body", "retry",
		"misfire_policy", "catch_up_limit", "overlap_policy", "paused", "next_due_at", "version",
	}
	assertFields(t, properties, want, "Schedule")
	required := scalarStrings(t, schedule["required"])
	for _, field := range want {
		if field == "schedule" {
			continue
		} // The exclusive trigger branches cover schedule versus run_at.
		if !contains(required, field) {
			t.Errorf("Schedule response does not require concrete field %s", field)
		}
	}
	retryReference := object(t, properties, "retry")["$ref"]
	if retryReference != "#/components/schemas/ResolvedRetryPolicy" {
		t.Fatalf("Schedule response retry schema = %#v", retryReference)
	}
	resolvedRetry := object(t, schemas, "ResolvedRetryPolicy")
	requiredRetry := scalarStrings(t, resolvedRetry["required"])
	for _, field := range []string{"max_attempts", "backoff_seconds", "max_backoff_seconds", "max_retry_window_seconds"} {
		if !contains(requiredRetry, field) {
			t.Errorf("resolved retry response does not require %s", field)
		}
	}
}

func TestOccurrenceAndDispatchProtocolAreMachineReadable(t *testing.T) {
	document := loadOpenAPI(t)
	schemas := object(t, document.Components, "schemas")
	occurrenceProperties := object(t, object(t, schemas, "Occurrence"), "properties")
	for _, field := range []string{"id", "tenant_id", "schedule_id", "schedule_name", "scheduled_at", "observed_at", "status", "outcome_code", "missed_count", "attempt_count", "last_http_status", "last_error_code", "last_error_message", "created_at", "updated_at", "completed_at"} {
		if _, found := occurrenceProperties[field]; !found {
			t.Errorf("Occurrence is missing %s", field)
		}
	}
	dispatch, found := document.Webhooks["scheduleOccurrenceDispatch"]
	if !found {
		t.Fatal("dispatch webhook contract is missing")
	}
	for _, method := range []string{"post", "put"} {
		operation := object(t, dispatch, method)
		if operation["x-kokoro-idempotency"] != "stable-occurrence-key" {
			t.Errorf("dispatch %s lacks stable occurrence idempotency", method)
		}
		got := scalarStrings(t, operation["x-kokoro-retryable-statuses"])
		for _, status := range []string{"408", "425", "429", "5xx"} {
			if !contains(got, status) {
				t.Errorf("dispatch %s retry contract is missing %s", method, status)
			}
		}
	}
}

func TestBreakingPolicyProtectsCurrentRequiredSurface(t *testing.T) {
	document := loadOpenAPI(t)
	var policy struct {
		ContractMajor               int                            `json:"contract_major"`
		RequiredPaths               map[string][]string            `json:"required_paths"`
		RequiredScheduleInputFields []string                       `json:"required_schedule_input_fields"`
		RequiredScheduleFields      []string                       `json:"required_schedule_response_fields"`
		RequiredResolvedRetryFields []string                       `json:"required_resolved_retry_fields"`
		RequiredOccurrenceFields    []string                       `json:"required_occurrence_fields"`
		RequiredDispatchHeaders     []string                       `json:"required_dispatch_headers"`
		RequiredRetryableStatuses   []string                       `json:"required_retryable_statuses"`
		RequiredControlErrors       map[string]map[string][]string `json:"required_control_errors"`
	}
	readJSON(t, repositoryPath(t, "contract/openapi/v1/breaking-policy.json"), &policy)
	if !strings.HasPrefix(document.Info.Version, strconv.Itoa(policy.ContractMajor)+".") {
		t.Fatalf("contract version %q does not satisfy breaking-policy major %d", document.Info.Version, policy.ContractMajor)
	}
	for path, methods := range policy.RequiredPaths {
		item, found := document.Paths[path]
		if !found {
			t.Errorf("breaking change removed path %s", path)
			continue
		}
		for _, method := range methods {
			if _, found := item[method]; !found {
				t.Errorf("breaking change removed %s %s", method, path)
			}
		}
	}
	schemas := object(t, document.Components, "schemas")
	assertFields(t, object(t, object(t, schemas, "ScheduleInput"), "properties"), policy.RequiredScheduleInputFields, "ScheduleInput")
	if len(policy.RequiredScheduleFields) == 0 || len(policy.RequiredResolvedRetryFields) == 0 {
		t.Fatal("breaking policy must protect resolved Schedule and retry response fields")
	}
	assertFields(t, object(t, object(t, schemas, "Schedule"), "properties"), policy.RequiredScheduleFields, "Schedule")
	assertFields(t, object(t, object(t, schemas, "ResolvedRetryPolicy"), "properties"), policy.RequiredResolvedRetryFields, "ResolvedRetryPolicy")
	assertFields(t, object(t, object(t, schemas, "Occurrence"), "properties"), policy.RequiredOccurrenceFields, "Occurrence")
	post := object(t, document.Webhooks["scheduleOccurrenceDispatch"], "post")
	headerNames := referencedParameterNames(t, document, post["parameters"])
	for _, header := range policy.RequiredDispatchHeaders {
		if !contains(headerNames, header) {
			t.Errorf("breaking change removed dispatch header %s", header)
		}
	}
	retryable := scalarStrings(t, post["x-kokoro-retryable-statuses"])
	for _, status := range policy.RequiredRetryableStatuses {
		if !contains(retryable, status) {
			t.Errorf("breaking change removed retryable dispatch status %s", status)
		}
	}
	if len(policy.RequiredControlErrors) == 0 {
		t.Fatal("breaking policy must protect stable control error codes by operation and HTTP status")
	}
	declared := make(map[string]map[string][]string)
	stableCodes := make(map[string]struct{})
	for _, item := range document.Paths {
		for method, raw := range item {
			if method == "parameters" {
				continue
			}
			operation := raw.(map[string]any)
			operationID := operation["operationId"].(string)
			_, found := operation["x-kokoro-control-error-codes"]
			if !found {
				continue
			}
			errorsByStatus := object(t, operation, "x-kokoro-control-error-codes")
			declared[operationID] = make(map[string][]string, len(errorsByStatus))
			responses := object(t, operation, "responses")
			for status, rawCodes := range errorsByStatus {
				codes := scalarStrings(t, rawCodes)
				if len(codes) == 0 {
					t.Fatalf("%s error %s has no codes", operationID, status)
				}
				if _, found := responses[status]; !found {
					t.Errorf("%s maps errors to undeclared %s", operationID, status)
				}
				declared[operationID][status] = codes
				for _, code := range codes {
					if code == "" {
						t.Fatal("empty stable control code")
					}
					stableCodes[code] = struct{}{}
				}
			}
		}
	}
	if !reflect.DeepEqual(declared, policy.RequiredControlErrors) {
		t.Fatalf("OpenAPI control errors %#v do not match breaking policy %#v", declared, policy.RequiredControlErrors)
	}
	for _, code := range []string{"schedule_already_exists", "schedule_not_found", "schedule_exhausted", "schedule_changed"} {
		if _, found := stableCodes[code]; !found {
			t.Errorf("stable control error set is missing %s", code)
		}
	}
	if len(stableCodes) != 4 {
		t.Fatalf("stable control error set = %#v, want exactly four owner control codes", stableCodes)
	}
}

func TestContractProvenanceDigestMatchesCanonicalArtifact(t *testing.T) {
	var manifest struct {
		Owner           string `json:"owner"`
		Artifact        string `json:"artifact"`
		ContractVersion string `json:"contract_version"`
		SourceSHA256    string `json:"source_sha256"`
		BreakingPolicy  string `json:"breaking_policy"`
		PolicySHA256    string `json:"breaking_policy_sha256"`
	}
	readJSON(t, repositoryPath(t, "contract/manifest.json"), &manifest)
	if manifest.Owner != "kokoro-scheduler" || manifest.Artifact != "contract/openapi/v1/openapi.yaml" || manifest.ContractVersion != "3.0.0" || manifest.BreakingPolicy != "contract/openapi/v1/breaking-policy.json" {
		t.Fatalf("contract provenance = %#v", manifest)
	}
	body, err := os.ReadFile(repositoryPath(t, manifest.Artifact))
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	if manifest.SourceSHA256 != digest {
		t.Fatalf("contract digest = %s, want %s", manifest.SourceSHA256, digest)
	}
	policy, err := os.ReadFile(repositoryPath(t, manifest.BreakingPolicy))
	if err != nil {
		t.Fatal(err)
	}
	policyDigest := fmt.Sprintf("%x", sha256.Sum256(policy))
	if manifest.PolicySHA256 != policyDigest {
		t.Fatalf("breaking policy digest = %s, want %s", manifest.PolicySHA256, policyDigest)
	}
}

func TestAllLocalOpenAPIReferencesResolve(t *testing.T) {
	var document map[string]any
	readJSON(t, repositoryPath(t, "contract/openapi/v1/openapi.yaml"), &document)
	walkReferences(t, document, document)
}

type openAPIDocument struct {
	OpenAPI    string                    `json:"openapi"`
	Info       struct{ Version string }  `json:"info"`
	Paths      map[string]map[string]any `json:"paths"`
	Webhooks   map[string]map[string]any `json:"webhooks"`
	Components map[string]any            `json:"components"`
}

func loadOpenAPI(t *testing.T) openAPIDocument {
	t.Helper()
	var document openAPIDocument
	readJSON(t, repositoryPath(t, "contract/openapi/v1/openapi.yaml"), &document)
	return document
}

func repositoryPath(t *testing.T, relative string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../..", relative))
}

func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func object(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object", key)
	}
	return value
}

func assertFields(t *testing.T, properties map[string]any, required []string, schema string) {
	t.Helper()
	for _, field := range required {
		if _, found := properties[field]; !found {
			t.Errorf("breaking change removed %s.%s", schema, field)
		}
	}
}

func scalarStrings(t *testing.T, value any) []string {
	t.Helper()
	values, ok := value.([]any)
	if !ok {
		t.Fatalf("value %#v is not an array", value)
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		switch typed := item.(type) {
		case string:
			result = append(result, typed)
		case float64:
			result = append(result, strconv.Itoa(int(typed)))
		default:
			t.Fatalf("array item %#v is not a scalar", item)
		}
	}
	return result
}

func referencedParameterNames(t *testing.T, document openAPIDocument, value any) []string {
	t.Helper()
	references, ok := value.([]any)
	if !ok {
		t.Fatalf("parameters %#v are not an array", value)
	}
	parameters := object(t, document.Components, "parameters")
	result := make([]string, 0, len(references))
	for _, raw := range references {
		reference, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("parameter %#v is not an object", raw)
		}
		path, ok := reference["$ref"].(string)
		if !ok || !strings.HasPrefix(path, "#/components/parameters/") {
			t.Fatalf("parameter reference = %#v", reference)
		}
		name := strings.TrimPrefix(path, "#/components/parameters/")
		parameter := object(t, parameters, name)
		parameterName, ok := parameter["name"].(string)
		if !ok {
			t.Fatalf("parameter %s has no string name", name)
		}
		result = append(result, parameterName)
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func walkReferences(t *testing.T, root map[string]any, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				reference, ok := child.(string)
				if !ok || !strings.HasPrefix(reference, "#/") || !referenceResolves(root, reference) {
					t.Errorf("unresolved OpenAPI reference %#v", child)
				}
				continue
			}
			walkReferences(t, root, child)
		}
	case []any:
		for _, child := range typed {
			walkReferences(t, root, child)
		}
	}
}

func referenceResolves(root map[string]any, reference string) bool {
	var current any = root
	for _, token := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		object, ok := current.(map[string]any)
		if !ok {
			return false
		}
		current, ok = object[strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")]
		if !ok {
			return false
		}
	}
	return true
}

func rejectDuplicateKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("OpenAPI object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("canonical OpenAPI contains duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected OpenAPI delimiter %q", delimiter)
	}
}

func TestCurrentScheduleReadIsNotAMutationReceipt(t *testing.T) {
	document := loadOpenAPI(t)
	item := document.Paths["/internal/scheduler/v1/schedules/{name}"]
	get := object(t, item, "get")
	if _, hasBody := get["requestBody"]; hasBody {
		t.Fatal("GET declares a command body")
	}
	for _, parameters := range []any{item["parameters"], get["parameters"]} {
		if parameters == nil {
			continue
		}
		for _, name := range referencedParameterNames(t, document, parameters) {
			if name == "Idempotency-Key" {
				t.Fatal("GET inherits a mutation identity")
			}
		}
	}
	if get["x-kokoro-idempotency"] != "read-only" {
		t.Fatal("GET must be a read-only query")
	}
	responses := object(t, get, "responses")
	if object(t, responses, "200")["$ref"] != "#/components/responses/CurrentSchedule" {
		t.Fatal("GET uses an immutable command ACK")
	}
	for _, code := range []string{"400", "401", "404", "500"} {
		if _, ok := responses[code]; !ok {
			t.Errorf("GET missing %s", code)
		}
	}
	schemas := object(t, document.Components, "schemas")
	current := object(t, schemas, "CurrentSchedule")
	if current["additionalProperties"] != false {
		t.Fatal("current schedule must be closed")
	}
	required := scalarStrings(t, current["required"])
	if !contains(required, "status") || !contains(required, "next_due_at") {
		t.Fatal("current state/next must be explicit")
	}
	base := object(t, schemas, "Schedule")
	properties := object(t, current, "properties")
	baseProperties := object(t, base, "properties")
	if len(properties) != len(baseProperties)+1 {
		t.Fatal("current schedule duplicates or drops base fields")
	}
	for field, definition := range baseProperties {
		if field == "next_due_at" {
			continue
		} // Current exhausted state has an explicit nullable next.
		if !reflect.DeepEqual(properties[field], definition) {
			t.Errorf("current schedule drifted at %s", field)
		}
	}
	status := object(t, object(t, current, "properties"), "status")
	if got := strings.Join(scalarStrings(t, status["enum"]), ","); got != "active,paused,exhausted" {
		t.Fatalf("status=%s", got)
	}
	response := object(t, object(t, document.Components, "responses"), "CurrentSchedule")
	cache := object(t, object(t, response, "headers"), "Cache-Control")
	if object(t, cache, "schema")["const"] != "no-store" {
		t.Fatal("GET must not be cached")
	}
}

func TestFiniteScheduleContractRequiresExclusiveTriggerAndExhaustedNullNext(t *testing.T) {
	document := loadOpenAPI(t)
	schemas := object(t, document.Components, "schemas")
	for _, name := range []string{"ScheduleInput", "Schedule", "CurrentSchedule"} {
		schema := object(t, schemas, name)
		properties := object(t, schema, "properties")
		if _, ok := properties["run_at"]; !ok {
			t.Fatalf("%s missing absolute trigger", name)
		}
		if contains(scalarStrings(t, schema["required"]), "schedule") {
			t.Fatalf("%s still forces recurring cron", name)
		}
		branches, ok := schema["oneOf"].([]any)
		if !ok || len(branches) != 2 {
			t.Fatalf("%s missing exclusive triggers", name)
		}
		for i, field := range []string{"schedule", "run_at"} {
			branch, ok := branches[i].(map[string]any)
			if !ok || !reflect.DeepEqual(scalarStrings(t, branch["required"]), []string{field}) {
				t.Fatalf("%s trigger branch=%#v", name, branches[i])
			}
		}
	}
	current := object(t, schemas, "CurrentSchedule")
	conditions, ok := current["allOf"].([]any)
	if !ok || len(conditions) != 1 {
		t.Fatal("current status/next lifecycle constraint is missing")
	}
	conditional := conditions[0].(map[string]any)
	then := object(t, conditional, "then")
	if object(t, object(t, then, "properties"), "next_due_at")["type"] != "null" || !contains(scalarStrings(t, then["required"]), "run_at") {
		t.Fatal("exhausted must be once and have no next")
	}
	if object(t, object(t, object(t, conditional, "else"), "properties"), "next_due_at")["type"] != "string" {
		t.Fatal("live schedules must retain their actual next")
	}
	ack := object(t, schemas, "Schedule")
	if !reflect.DeepEqual(scalarStrings(t, object(t, object(t, ack, "properties"), "next_due_at")["type"]), []string{"string", "null"}) {
		t.Fatal("mutation ACK cannot describe a consumed once edit")
	}
	conditions, ok = ack["allOf"].([]any)
	if !ok || len(conditions) != 1 {
		t.Fatal("mutation null next must be limited to consumed once")
	}
	guard := conditions[0].(map[string]any)
	if object(t, object(t, object(t, guard, "if"), "properties"), "next_due_at")["type"] != "null" {
		t.Fatal("mutation lifecycle guard must inspect null next")
	}
	consumed := object(t, guard, "then")
	if !contains(scalarStrings(t, consumed["required"]), "run_at") || object(t, object(t, consumed, "properties"), "paused")["const"] != false {
		t.Fatal("consumed ACK must be once and not imply paused/live")
	}
	if _, exists := object(t, ack, "properties")["status"]; exists {
		t.Fatal("mutation ACK must retain the historical field set")
	}
}
