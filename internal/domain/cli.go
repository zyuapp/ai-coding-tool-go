package domain

import (
	"encoding/base64"
	"regexp"
	"strconv"
	"strings"

	whatwg "github.com/nationallibraryofnorway/whatwg-url/url"
)

const CliCommand = "aic"
const CliInstallPath = "/usr/local/bin/aic"
const CliURLScheme = "aicodingtool"

// CliScriptMarker is stamped into the script so an install can tell its own file from someone else's `aic`. Older stamps still count.
const CliScriptMarker = "# aic-cli v3"
const cliScriptStamp = "# aic-cli v"

// ServeRestartExit is what `aic serve` exits with to be started again by the script.
const ServeRestartExit = 75

// ServeRestartsEnv is set by a script that restarts `aic serve` on ServeRestartExit.
const ServeRestartsEnv = "AIC_SERVE_RESTARTS"

// ServeBootstrap is run by the app binary as Node: it finds the packaged serve entry beside it and starts it.
const ServeBootstrap = `(function(){var p=require("path"),f=require("fs"),d=p.dirname(process.execPath);var c=[p.join(d,"resources"),p.join(d,"..","Resources")];for(var i=0;i<c.length;i++){var s=p.join(c[i],"app.asar","dist","main","main","serve.js");if(f.existsSync(s))return require(s).cli()}console.error("aic: could not find AI Coding Tool beside "+process.execPath);process.exit(1)})()`

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func refusal() []string {
	return []string{"    printf '" + CliCommand + ": this install cannot serve. Install the command again from AI Coding Tool.\\n' >&2", "    exit 1"}
}

func appAsNode(executable string) string {
	return shellQuote(executable) + " -e " + shellQuote(ServeBootstrap) + ` -- "$@"`
}

// serveLines takes "" for no executable.
func serveLines(executable string) []string {
	if executable == "" {
		return refusal()
	}
	return []string{
		"    env " + ServeRestartsEnv + "=1 ELECTRON_RUN_AS_NODE=1 " + appAsNode(executable) + " <&0 &",
		"    child=$!",
		"    trap : INT",
		`    trap 'kill -TERM "$child" 2>/dev/null' TERM HUP`,
		`    wait "$child"; status=$?`,
		`    while kill -0 "$child" 2>/dev/null; do wait "$child"; status=$?; done`,
		`    [ "$status" -eq ` + strconv.Itoa(ServeRestartExit) + ` ] && exec "$0" "$@"`,
		`    exit "$status"`,
	}
}

func pairLines(executable string) []string {
	if executable == "" {
		return refusal()
	}
	return []string{"    exec env ELECTRON_RUN_AS_NODE=1 " + appAsNode(executable)}
}

func cliScript(opener []string, executable string) string {
	lines := []string{
		"#!/bin/sh",
		CliScriptMarker,
		"# Opens a folder as an AI Coding Tool project, or serves the app headless. Installed from AI Coding Tool settings.",
		`case "$1" in`,
		"  serve)",
	}
	lines = append(lines, serveLines(executable)...)
	lines = append(lines, "    ;;", "  pair)")
	lines = append(lines, pairLines(executable)...)
	lines = append(lines,
		"    ;;",
		"esac",
		"target=$1",
		`[ -n "$target" ] || target=.`,
		`if [ ! -d "$target" ]; then`,
		"  printf '"+CliCommand+`: not a directory: %s\n' "$target" >&2`,
		"  exit 1",
		"fi",
		`dir=$(cd "$target" && pwd)`,
		`encoded=$(printf %s "$dir" | base64 | tr -d '\n' | tr '+/' '-_')`,
	)
	lines = append(lines, opener...)
	lines = append(lines, "")
	return strings.Join(lines, "\n")
}

func MacCliScript(executable string) string {
	return cliScript([]string{`exec open "` + CliURLScheme + `://open?path=$encoded"`}, executable)
}

func LinuxCliScript(executable string) string {
	return cliScript([]string{
		`url="` + CliURLScheme + `://open?path=$encoded"`,
		`if command -v gio >/dev/null 2>&1; then exec gio open "$url"; fi`,
		`if command -v xdg-open >/dev/null 2>&1; then exec xdg-open "$url"; fi`,
		"printf '" + CliCommand + ": could not find xdg-open or gio to open AI Coding Tool.\\n' >&2",
		"exit 1",
	}, executable)
}

// CliCliConfiguration is nil when the platform has no terminal integration. An empty executable refuses to serve.
func CliCliConfiguration(platform, homeDirectory, executable string) *CliConfiguration {
	if platform == "darwin" {
		return &CliConfiguration{InstallPath: CliInstallPath, Script: MacCliScript(executable)}
	}
	if platform != "linux" || !strings.HasPrefix(homeDirectory, "/") {
		return nil
	}
	home := strings.TrimRight(homeDirectory, "/")
	if home == "" {
		home = "/"
	}
	if home == "/" {
		home = ""
	}
	return &CliConfiguration{InstallPath: home + "/.local/bin/" + CliCommand, Script: LinuxCliScript(executable)}
}

func IsCliScript(contents string) bool {
	return strings.Contains(contents, cliScriptStamp)
}

var cliEncodedPathPattern = regexp.MustCompile(`^[A-Za-z0-9\-_=]+$`)

// ProjectPathFromURL is the folder an `aicodingtool://open?path=` URL names; false when the URL is not one we wrote.
func ProjectPathFromURL(value string) (string, bool) {
	url, err := whatwg.Parse(value)
	if err != nil {
		return "", false
	}
	if url.Protocol() != CliURLScheme+":" || url.Hostname() != "open" {
		return "", false
	}
	encoded := url.SearchParams().Get("path")
	if encoded == "" || utf16Length(encoded) > 8_192 || !cliEncodedPathPattern.MatchString(encoded) {
		return "", false
	}
	binary, ok := cliAtob(strings.NewReplacer("-", "+", "_", "/").Replace(encoded))
	if !ok {
		return "", false
	}
	decoded := strings.ToValidUTF8(strings.TrimPrefix(string(binary), "\uFEFF"), "\uFFFD")
	if !strings.HasPrefix(decoded, "/") || strings.Contains(decoded, "\x00") {
		return "", false
	}
	return decoded, true
}

// cliAtob decodes base64 the forgiving way atob does: padding optional, stray bits ignored.
func cliAtob(encoded string) ([]byte, bool) {
	if len(encoded)%4 == 0 {
		encoded = strings.TrimSuffix(encoded, "=")
		encoded = strings.TrimSuffix(encoded, "=")
	}
	if len(encoded)%4 == 1 {
		return nil, false
	}
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	return decoded, err == nil
}

func ProjectPathFromArgv(argv []string) (string, bool) {
	for _, argument := range argv {
		if !strings.HasPrefix(argument, CliURLScheme+"://") {
			continue
		}
		if root, ok := ProjectPathFromURL(argument); ok && root != "" {
			return root, true
		}
	}
	return "", false
}
