package im

import (
	"strings"
	"testing"

	actioncontract "github.com/domainry/domainry-foundation/action"
	"github.com/domainry/domainry-foundation/modulehttp"
)

func TestAuthorizationActionsOwnEveryHTTPRoute(t *testing.T) {
	definitions, err := AuthorizationActions()
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 18 {
		t.Fatalf("actions=%d want=18", len(definitions))
	}
	seen := map[string]bool{}
	for _, definition := range definitions {
		if definition.Owner != AuthorizationOwner {
			t.Fatalf("invalid exact permission: %#v", definition)
		}
		if definition.Key == ActionContextMessagesSend {
			if definition.Permission != nil || definition.Authorization.Strategy != actioncontract.AuthorizationSigned || definition.Authorization.PolicyKey != "identity.application_service" {
				t.Fatalf("invalid signed projection action: %#v", definition)
			}
		} else if definition.Permission == nil || definition.Permission.Key != definition.Key || definition.Permission.GrantMode != actioncontract.PermissionGrantDirect {
			t.Fatalf("invalid exact permission: %#v", definition)
		}
		route, err := modulehttp.RouteFromAction(definition)
		if err != nil {
			t.Fatal(err)
		}
		if seen[route.Pattern()] {
			t.Fatalf("duplicate route %s", route.Pattern())
		}
		if strings.Contains(route.Pattern(), "event-tickets") {
			t.Fatal("credential-bearing SSE ticket route must not exist")
		}
		seen[route.Pattern()] = true
	}
	for _, pattern := range []string{
		"GET /im/events",
		"PUT /im/threads/{thread_id}/members",
		"GET /im/contexts/{namespace}/{resource_type}/{resource_id}/history",
		"GET /im/contexts/{namespace}/{resource_type}/{resource_id}/history/search",
		"POST /im/contexts/{namespace}/{resource_type}/{resource_id}/messages",
		"PUT /im/contexts/{namespace}/{resource_type}/{resource_id}/members",
		"GET /im/contexts/{namespace}/{resource_type}/{resource_id}/attachments/{attachment_id}/content",
		"GET /im/quick-replies",
		"PUT /im/quick-replies/{key}",
	} {
		if !seen[pattern] {
			t.Fatalf("required IM route %s is missing", pattern)
		}
	}
}
