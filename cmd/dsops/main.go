// Command dsops generates and uses the key material connectors authenticate
// with. Two subcommands, because two are what a deployment needs: one to
// create a participant's key, and one to mint a credential by hand — for a
// test harness, or to check a roster entry without standing a connector up.
//
// It deliberately does not manage the roster. A roster is a small JSON file
// that an operator edits and diffs in git (DECISIONS.md section 9), and a
// command that rewrote it would put a tool between the operator and a file
// they are meant to read.
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/auth"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

const usage = `usage:
  dsops keygen -out <path>
        Write a new Ed25519 private key and print its public half, which is
        what goes in a counterparty's roster.

  dsops token -key <path> -iss <participant> -aud <participant> [-ttl 5m]
        Mint a credential. Prints the token, with no "Bearer " prefix.`

func run(args []string, out *os.File) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	switch args[0] {
	case "keygen":
		return keygen(args[1:], out)
	case "token":
		return token(args[1:], out)
	default:
		return fmt.Errorf("unknown subcommand %q\n\n%s", args[0], usage)
	}
}

func keygen(args []string, out *os.File) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	path := fs.String("out", "", "where to write the private key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return fmt.Errorf("keygen: -out is required")
	}
	pub, err := auth.GenerateKeyFile(*path)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, base64.RawURLEncoding.EncodeToString(pub))
	return err
}

func token(args []string, out *os.File) error {
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	key := fs.String("key", "", "path to the signing key")
	iss := fs.String("iss", "", "the participant this credential is from")
	aud := fs.String("aud", "", "the participant this credential is for")
	ttl := fs.Duration("ttl", 5*time.Minute, "how long the credential stays valid")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *key == "" || *iss == "" || *aud == "" {
		return fmt.Errorf("token: -key, -iss, and -aud are all required")
	}
	priv, err := auth.LoadPrivateKey(*key)
	if err != nil {
		return err
	}
	tok, err := auth.Mint(priv, *iss, *aud, time.Now(), *ttl)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, tok)
	return err
}
