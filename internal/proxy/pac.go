package proxy

import (
	"bytes"
	"fmt"
	"net/http"
	"text/template"
)

const pacTemplateString = `function FindProxyForURL(url, host) {
{{- range .ClusterNames}}
  if (shExpMatch(host, "*.{{.}}"))
    return "{{$.ProxyDirective}}";
{{- end}}
  return "DIRECT";
}
`

var pacTemplate = template.Must(template.New("pac").Parse(pacTemplateString))

// PACServer serves an auto-generated PAC (Proxy Auto-Configuration) file
// that routes traffic for configured cluster domains through the proxy.
type PACServer struct {
	ClusterNames     []string
	SOCKSAddress     string
	HTTPProxyAddress string
}

func (s *PACServer) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
	w.Header().Set("Content-Disposition", "inline; filename=\"proxy.pac\"")
	_, _ = fmt.Fprint(w, s.generatePAC())
}

func (s *PACServer) generatePAC() string {
	if len(s.ClusterNames) == 0 {
		return "function FindProxyForURL(url, host) {\n  return \"DIRECT\";\n}\n"
	}

	data := struct {
		ClusterNames   []string
		ProxyDirective string
	}{
		ClusterNames:   s.ClusterNames,
		ProxyDirective: s.proxyDirective(),
	}

	var buf bytes.Buffer
	if err := pacTemplate.Execute(&buf, data); err != nil {
		return fmt.Sprintf("// error generating PAC: %v\n", err)
	}

	return buf.String()
}

func (s *PACServer) proxyDirective() string {
	if s.HTTPProxyAddress != "" {
		return fmt.Sprintf("PROXY %s; SOCKS5 %s; DIRECT", s.HTTPProxyAddress, s.SOCKSAddress)
	}

	return fmt.Sprintf("SOCKS5 %s; DIRECT", s.SOCKSAddress)
}
