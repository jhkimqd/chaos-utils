package http

import (
	"fmt"
	"strings"
)

// generateEnvoyConfig builds an Envoy v3 bootstrap config with fault injection.
func generateEnvoyConfig(params HTTPFaultParams) string {
	proxyPort := params.proxyPort()

	// Build route match clause (indented to level under route entry)
	routeMatch := "prefix: \"/\""
	if params.PathPattern != "" {
		if strings.HasPrefix(params.PathPattern, "~") {
			routeMatch = fmt.Sprintf("safe_regex:\n                          regex: \"%s\"", params.PathPattern[1:])
		} else {
			routeMatch = fmt.Sprintf("prefix: \"%s\"", params.PathPattern)
		}
	}

	// Build http_filters section
	var filterLines []string

	// Fault filter
	if params.DelayMs > 0 || params.AbortCode > 0 {
		filterLines = append(filterLines,
			"                - name: envoy.filters.http.fault",
			"                  typed_config:",
			"                    \"@type\": type.googleapis.com/envoy.extensions.filters.http.fault.v3.HTTPFault",
		)
		if params.DelayMs > 0 {
			dp := params.DelayPercent
			if dp <= 0 {
				dp = 100
			}
			// Envoy protobuf Duration requires seconds with 's' suffix
			delaySec := fmt.Sprintf("%.3fs", float64(params.DelayMs)/1000.0)
			filterLines = append(filterLines,
				"                    delay:",
				fmt.Sprintf("                      fixed_delay: %s", delaySec),
				"                      percentage:",
				fmt.Sprintf("                        numerator: %d", dp),
				"                        denominator: HUNDRED",
			)
		}
		if params.AbortCode > 0 {
			ap := params.AbortPercent
			if ap <= 0 {
				ap = 100
			}
			filterLines = append(filterLines,
				"                    abort:",
				fmt.Sprintf("                      http_status: %d", params.AbortCode),
				"                      percentage:",
				fmt.Sprintf("                        numerator: %d", ap),
				"                        denominator: HUNDRED",
			)
		}
	}

	// Lua filter for body/header override
	if params.BodyOverride != "" || len(params.HeaderOverrides) > 0 {
		filterLines = append(filterLines, buildLuaFilter(params)...)
	}

	// Router (always last)
	filterLines = append(filterLines,
		"                - name: envoy.filters.http.router",
		"                  typed_config:",
		"                    \"@type\": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router",
	)

	filtersStr := strings.Join(filterLines, "\n")

	config := fmt.Sprintf(`admin:
  address:
    socket_address:
      address: 127.0.0.1
      port_value: %d

static_resources:
  listeners:
    - name: chaos_listener
      address:
        socket_address:
          address: 0.0.0.0
          port_value: %d
      filter_chains:
        - filters:
            - name: envoy.filters.network.http_connection_manager
              typed_config:
                "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
                stat_prefix: chaos_http
                use_remote_address: false
                route_config:
                  name: local_route
                  virtual_hosts:
                    - name: backend
                      domains: ["*"]
                      routes:
                        - match:
                            %s
                          route:
                            cluster: local_backend
                            timeout: 60s
                http_filters:
%s

  clusters:
    - name: local_backend
      connect_timeout: 5s
      type: STATIC
      load_assignment:
        cluster_name: local_backend
        endpoints:
          - lb_endpoints:
              - endpoint:
                  address:
                    socket_address:
                      address: 127.0.0.1
                      port_value: %d
`,
		proxyPort+1,
		proxyPort,
		routeMatch,
		filtersStr,
		params.TargetPort,
	)

	return config
}

// buildLuaFilter generates Envoy Lua filter lines
func buildLuaFilter(params HTTPFaultParams) []string {
	var luaParts []string
	luaParts = append(luaParts, "function envoy_on_response(response_handle)")

	for key, value := range params.HeaderOverrides {
		luaParts = append(luaParts, fmt.Sprintf("  response_handle:headers():replace(\"%s\", \"%s\")",
			escapeLua(key), escapeLua(value)))
	}

	if params.BodyOverride != "" {
		luaParts = append(luaParts, fmt.Sprintf("  local body = \"%s\"", escapeLua(params.BodyOverride)))
		luaParts = append(luaParts, "  response_handle:headers():replace(\"content-length\", tostring(#body))")
		luaParts = append(luaParts, "  local orig = response_handle:body()")
		luaParts = append(luaParts, "  if orig then")
		luaParts = append(luaParts, "    orig:setBytes(body)")
		luaParts = append(luaParts, "  end")
	}

	luaParts = append(luaParts, "end")

	// Build the filter YAML lines
	lines := []string{
		"                - name: envoy.filters.http.lua",
		"                  typed_config:",
		"                    \"@type\": type.googleapis.com/envoy.extensions.filters.http.lua.v3.Lua",
		"                    default_source_code:",
		"                      inline_string: |",
	}
	for _, l := range luaParts {
		lines = append(lines, "                        "+l)
	}

	return lines
}

// escapeLua escapes a string for embedding in a Lua string literal
func escapeLua(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	return s
}
