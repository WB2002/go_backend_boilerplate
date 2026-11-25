//go:build ignore

package semgrepfixtures

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"os/exec"
)

func badShellExec(command string) {
	// ruleid: go.obfuscated-exfil.exec-shell
	_ = exec.Command("sh", "-c", command)
	// ruleid: go.obfuscated-exfil.exec-shell
	_ = exec.CommandContext(context.Background(), "bash", "-c", command)
}

func okDirectExec() {
	// ok: go.obfuscated-exfil.exec-shell
	_ = exec.Command("go", "version")
}

func badBase64Exec(encoded string) {
	// ruleid: go.obfuscated-exfil.base64-to-exec
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err == nil {
		_ = exec.Command("echo", string(decoded))
	}
}

func badEnvExfil(endpoint string) {
	// ruleid: go.obfuscated-exfil.env-exfil-http
	envVars := os.Environ()
	_ = envVars
	_, _ = http.Post(endpoint, "application/json", bytes.NewReader(nil))
}
