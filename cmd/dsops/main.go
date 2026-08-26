// Command dsops generates and uses the key material connectors authenticate
// with: a participant's key, a credential minted by hand — for a test
// harness, or to check a roster entry without standing a connector up — a
// roster's signature, and a counterparty's key resolved from its did:web
// identifier.
//
// It deliberately does not manage the roster. A roster is a small JSON file
// that an operator edits and diffs in git (DECISIONS.md section 9), and a
// command that rewrote it would put a tool between the operator and a file
// they are meant to read. Signing is no exception: `roster sign` prints a
// signature for the operator to paste in, the same way `keygen` prints a
// public key rather than writing it into anyone's roster.
//
// `roster sign` does judge the file before it signs it, and that is not the
// same thing. It refuses what the connector would refuse about the document
// — a missing version, a missing or malformed expires_at, an expiry already
// past or further ahead than the connector accepts — because printing a
// signature for a roster that cannot be loaded hands the operator a success
// now and a failure days later. It stops at the document: SignRoster does
// not walk the participant entries, so a malformed one is still caught only
// at boot. It reads and reports; it still writes nothing, and it still does
// not decide who belongs in the roster or how long it should live.
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
        Mint a credential. Prints the token, with no "Bearer " prefix.
        -ttl is unbounded here and bounded at the far end: a connector
        refuses a credential whose expiry sits more than an hour ahead of its
        own clock, and its 401 says only that a credential is required, so an
        over-long token fails the same way a forged one does and the operator
        who minted it is not told which (DECISIONS.md section 37).

  dsops roster sign -roster <path> -key <path>
        Sign a roster with the operator's key. Prints the signature; it does
        not write the file — paste the printed value into the roster's own
        "signature" field.

  dsops resolve <did:web:...> [-allow-http]
        Resolve a did:web identifier to its Ed25519 public key over HTTPS.
        Prints the key in the same form keygen does, for pasting into a
        roster entry. -allow-http resolves over plain HTTP instead, for a
        server with no TLS to terminate (local demos and tests only).`

func run(args []string, out *os.File) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	switch args[0] {
	case "keygen":
		return keygen(args[1:], out)
	case "token":
		return token(args[1:], out)
	case "roster":
		return roster(args[1:], out)
	case "resolve":
		return resolve(args[1:], out)
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

// roster dispatches this subcommand's own verb — "sign" today, matching the
// keygen/token pattern of one flag set per verb rather than a shared one
// growing flags that only some verbs use.
func roster(args []string, out *os.File) error {
	if len(args) == 0 || args[0] != "sign" {
		return fmt.Errorf("roster: only \"sign\" is a known verb")
	}
	fs := flag.NewFlagSet("roster sign", flag.ContinueOnError)
	rosterPath := fs.String("roster", "", "path to the roster to sign")
	key := fs.String("key", "", "path to the operator's signing key")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *rosterPath == "" || *key == "" {
		return fmt.Errorf("roster sign: -roster and -key are both required")
	}
	priv, err := auth.LoadPrivateKey(*key)
	if err != nil {
		return err
	}
	sig, err := auth.SignRoster(*rosterPath, priv, time.Now())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, sig)
	return err
}

func resolve(args []string, out *os.File) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	allowHTTP := fs.Bool("allow-http", false, "resolve over plain HTTP instead of HTTPS")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("resolve: exactly one did:web identifier is required")
	}
	pub, err := auth.ResolveDIDWeb(fs.Arg(0), *allowHTTP)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, base64.RawURLEncoding.EncodeToString(pub))
	return err
}
