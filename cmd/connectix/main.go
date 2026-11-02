// Command connectix is the set-up tool for a Connectix integration: it
// checks a token, lists templates without their text, and registers or
// removes the integration and its webhook hooks.
//
// It reads CONNECTIX_TOKEN (the application's token for the host) and
// CONNECTIX_BASE_URL (the host, for example https://api-sandbox.connectix.bg)
// from the environment. Secrets are never arguments, which a process list
// shows: the integration secret and the integration token come on standard
// input, and the integration token is written to a file, never printed.
//
//	connectix me
//	connectix templates
//	connectix register-integration --platform <name> --out <file> [--kind custom]   (secret on stdin)
//	connectix register-hook --url <url> [--type callback]                           (integration token on stdin)
//	connectix unregister-hook --id <id>
//
// register-integration, register-hook and unregister-hook change the
// application's real data, on the sandbox host too.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	connectix "github.com/nimasystems/connectix-client"
)

const (
	usage = `usage: connectix <command> [flags]

commands:
  me                                     print the application's name
  templates                              print id, name, channel, status and slots of each template
  register-integration --platform <p> --out <file> [--kind custom|ecommerce|automation]
                                         read the 32-byte secret from stdin, write the integration token to <file>
  register-hook --url <url> [--type callback|inbound|template|tracking|blacklist]
                                         read the integration token from stdin, print the hook's id
  unregister-hook --id <id>              remove a hook

environment: CONNECTIX_TOKEN, CONNECTIX_BASE_URL
`

	// commandTimeout bounds a whole command, so it cannot hang.
	commandTimeout = 30 * time.Second

	// maxStdin bounds what is read from standard input: a secret or a token
	// is far shorter.
	maxStdin = 4096
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	cancel()
	os.Exit(code)
}

// run is the command with its world passed in, so tests drive it without a
// process. It returns the exit code: 0 done, 1 the call failed, 2 refused
// before calling.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "me", "templates", "register-integration", "register-hook", "unregister-hook":
	default:
		fmt.Fprintf(stderr, "connectix: unknown command\n\n%s", usage)
		return 2
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var platform, out, kind, hookURL, hookType, id string
	switch cmd {
	case "register-integration":
		fs.StringVar(&platform, "platform", "", "name of the system that integrates (required)")
		fs.StringVar(&out, "out", "", "file to write the integration token to, mode 0600 (required)")
		fs.StringVar(&kind, "kind", "custom", "custom, ecommerce or automation")
	case "register-hook":
		fs.StringVar(&hookURL, "url", "", "public URL that receives the webhook (required)")
		fs.StringVar(&hookType, "type", "callback", "callback, inbound, template, tracking or blacklist")
	case "unregister-hook":
		fs.StringVar(&id, "id", "", "the hook's id (required)")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stderr, usage)
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "connectix %s: unexpected arguments\n", cmd)
		return 2
	}
	switch {
	case cmd == "register-integration" && (platform == "" || out == ""):
		return refuse(stderr, "register-integration needs --platform and --out")
	case cmd == "register-hook" && hookURL == "":
		return refuse(stderr, "register-hook needs --url")
	case cmd == "unregister-hook" && id == "":
		return refuse(stderr, "unregister-hook needs --id")
	}

	token := getenv("CONNECTIX_TOKEN")
	if token == "" {
		return refuse(stderr, "CONNECTIX_TOKEN is not set")
	}
	base := getenv("CONNECTIX_BASE_URL")
	if base == "" {
		return refuse(stderr, "CONNECTIX_BASE_URL is not set")
	}
	client, err := connectix.New(token, connectix.WithBaseURL(base))
	if err != nil {
		return refuse(stderr, err.Error()) // New never echoes the token or the URL
	}

	switch cmd {
	case "me":
		name, err := client.Me(ctx)
		if err != nil {
			return failed(stderr, err)
		}
		fmt.Fprintln(stdout, name)
	case "templates":
		templates, err := client.ListTemplates(ctx)
		if err != nil {
			return failed(stderr, err)
		}
		for _, t := range templates { // never t.Text: it is the company's wording
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Name, t.Channel, t.Status, strings.Join(t.Slots, ","))
		}
	case "register-integration":
		return registerIntegration(ctx, client, platform, kind, out, stdin, stdout, stderr)
	case "register-hook":
		integrationToken, err := readLine(stdin)
		if err != nil {
			return refuse(stderr, "register-hook: "+err.Error())
		}
		hookID, err := client.RegisterHook(ctx, integrationToken, hookType, hookURL)
		if err != nil {
			return failed(stderr, err)
		}
		fmt.Fprintln(stdout, hookID)
	case "unregister-hook":
		if err := client.UnregisterHook(ctx, id); err != nil {
			return failed(stderr, err)
		}
		fmt.Fprintf(stdout, "unregistered %s\n", id)
	}
	return 0
}

// registerIntegration registers the integration and writes its token to out
// and nowhere else. The file is made before the call, exclusively, so a path
// that cannot be written (or that already holds a token) is found out before
// anything changes at Connectix, and a failed call leaves no file behind.
func registerIntegration(ctx context.Context, client *connectix.Client, platform, kind, out string, stdin io.Reader, stdout, stderr io.Writer) int {
	secret, err := readLine(stdin)
	if err != nil {
		return refuse(stderr, "register-integration: "+err.Error())
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	switch {
	case errors.Is(err, os.ErrExist):
		return refuse(stderr, "register-integration: the --out file exists; remove it first")
	case err != nil:
		return refuse(stderr, "register-integration: the --out file cannot be created")
	}
	token, err := client.RegisterIntegration(ctx, kind, platform, []byte(secret))
	if err != nil {
		_ = f.Close()
		_ = os.Remove(out)
		return failed(stderr, err)
	}
	if _, err := io.WriteString(f, token+"\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(out)
		return failed(stderr, errors.New("the token file could not be written; the integration is registered, repeat the command with the same secret to get the token again"))
	}
	if err := f.Close(); err != nil {
		return failed(stderr, errors.New("the token file could not be closed; check it before using it"))
	}
	fmt.Fprintf(stdout, "integration registered; the token is in %s\n", out)
	return 0
}

// readLine reads one value from standard input: its first line, without the
// line end. The value is a secret and is never echoed.
func readLine(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxStdin+1))
	if err != nil {
		return "", errors.New("standard input could not be read")
	}
	if len(b) > maxStdin {
		return "", errors.New("standard input is too long")
	}
	s := string(b)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, "\r")
	if s == "" {
		return "", errors.New("standard input is empty: the value is read from it")
	}
	return s, nil
}

func refuse(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "connectix: %s\n", msg)
	return 2
}

// failed reports a call that went wrong. The client's errors carry the HTTP
// status and no body, so they are safe to print as they are.
func failed(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "connectix: %v\n", err)
	return 1
}
