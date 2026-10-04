package gateway

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hgayan7/circuit/api"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestAgentContractMatchesWireFieldsAndIsPublished(t *testing.T) {
	var spec struct {
		Components struct {
			Schemas map[string]struct{ Properties map[string]any }
		}
	}
	require.NoError(t, yaml.Unmarshal(api.Contract, &spec))
	for name, value := range map[string]any{"ActionRequest": Request{}, "Action": Action{}, "Outcome": Outcome{}, "Reservation": Reservation{}} {
		typeOf := reflect.TypeOf(value)
		fields := map[string]any{}
		for i := 0; i < typeOf.NumField(); i++ {
			fields[strings.Split(typeOf.Field(i).Tag.Get("json"), ",")[0]] = true
		}
		require.Equal(t, len(fields), len(spec.Components.Schemas[name].Properties), name)
		for field := range fields {
			require.Contains(t, spec.Components.Schemas[name].Properties, field, name)
		}
	}
	s, _ := testService(t, testConfig(t, ""), &countingExecutor{})
	h, err := NewHTTPHandler(s, Tokens{Admin: adminToken, Agents: map[string]string{"agent": agentToken}})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))
	require.Equal(t, 200, w.Code)
	require.Equal(t, string(api.Contract), w.Body.String())
}
