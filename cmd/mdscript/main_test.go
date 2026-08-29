package main

import (
	"os"
	"strings"
	"testing"
)

func extractOrFail(t *testing.T, doc string) string {
	t.Helper()
	script, _, err := extract("doc.md", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	return script
}

func TestAnShBlockIsPastedVerbatim(t *testing.T) {
	script := extractOrFail(t, "prose\n\n```sh\necho hello\n```\n")
	if !strings.Contains(script, "echo hello\n") {
		t.Errorf("sh block did not reach the script:\n%s", script)
	}
}

func TestABlockWithNeitherShNorATitleIsIgnored(t *testing.T) {
	script := extractOrFail(t, "prose\n\n```\nrm -rf /\n```\n\n```text\nalso not script\n```\n")
	if strings.Contains(script, "rm -rf /") || strings.Contains(script, "also not script") {
		t.Errorf("a block that is neither sh nor a file became script:\n%s", script)
	}
}

// The quoting is the whole defence: a file block's content must reach the
// file unchanged, and an unquoted delimiter would let the shell run a
// backtick and expand a dollar sign inside what the reader sees as data.
func TestAFileBlockUsesAQuotedDelimiter(t *testing.T) {
	script := extractOrFail(t, "```yaml title=out.yaml\nkey: value\n```\n")
	if !strings.Contains(script, "cat > out.yaml <<'EOF'\n") {
		t.Errorf("file block did not use a quoted delimiter:\n%s", script)
	}
}

func TestAnExpandingFileBlockUsesAnUnquotedDelimiter(t *testing.T) {
	script := extractOrFail(t, "```yaml title=out.yaml expand\nkey: $VALUE\n```\n")
	if !strings.Contains(script, "cat > out.yaml <<EOF\n") {
		t.Errorf("expanding file block did not use an unquoted delimiter:\n%s", script)
	}
	if strings.Contains(script, "<<'EOF'") {
		t.Errorf("expanding file block was quoted, so its values would not expand:\n%s", script)
	}
}

func TestAFileBlockContainingTheDelimiterIsRefused(t *testing.T) {
	_, _, err := extract("doc.md", strings.NewReader("```yaml title=out.yaml\nkey: value\nEOF\nkey2: value2\n```\n"))
	if err == nil {
		t.Fatal("a block whose body ends the heredoc early was accepted")
	}
	if !strings.Contains(err.Error(), "doc.md:3") {
		t.Errorf("refusal does not name the line to look at: %v", err)
	}
}

func TestAnExpandingBlockEndingALineInABackslashIsRefused(t *testing.T) {
	_, _, err := extract("doc.md", strings.NewReader("```yaml title=out.yaml expand\nkey: $V\\\n```\n"))
	if err == nil {
		t.Fatal("a backslash that would escape the heredoc terminator was accepted")
	}
	if !strings.Contains(err.Error(), "doc.md:2") {
		t.Errorf("refusal does not name the line to look at: %v", err)
	}
}

// The counterpart, and the reason the guard above is scoped rather than
// global: a quoted heredoc does not treat a backslash as an escape, so
// refusing one there would reject a document that works.
func TestALiteralBlockMayEndALineInABackslash(t *testing.T) {
	script := extractOrFail(t, "```text title=out.txt\ntrailing \\\nnext line\n```\n")
	if !strings.Contains(script, "trailing \\\nnext line\n") {
		t.Errorf("a literal block with a trailing backslash did not survive:\n%s", script)
	}
}

func TestAnUnterminatedBlockIsRefused(t *testing.T) {
	_, _, err := extract("doc.md", strings.NewReader("```sh\necho hello\n"))
	if err == nil {
		t.Fatal("a document whose last block never closes was accepted")
	}
}

// The document is the artifact this command exists for, so a change to it
// that this command cannot process is a test failure here rather than a
// surprise in the quickstart job. It asserts extraction, not execution:
// running the script needs two connectors and a network address, which is
// what the quickstart CI job is for.
func TestTheQuickstartDocumentExtracts(t *testing.T) {
	f, err := os.Open("../../docs/quickstart.md")
	if err != nil {
		t.Fatalf("open the quickstart: %v", err)
	}
	defer f.Close()

	script, shape, err := extract("docs/quickstart.md", f)
	if err != nil {
		t.Fatalf("the quickstart no longer extracts: %v", err)
	}
	if shape.commands == 0 || shape.files == 0 {
		t.Errorf("the quickstart produced no commands or no files: %+v", shape)
	}
	// The transfer's own check is what makes the run mean anything; a
	// document that stopped asserting the bytes arrived would still extract.
	if !strings.Contains(script, "diff -q") {
		t.Error("the quickstart no longer compares the file that arrived against the one that was sent")
	}
	// The point of the document is that a reader is told nothing they have to
	// know in advance. The transfer format was the value that stayed out of
	// band longest, so a literal one here is the regression worth naming: the
	// exchange would still work and the claim would quietly be false.
	if strings.Contains(script, "HTTP-PULL") {
		t.Error("the quickstart hardcodes a transfer format instead of reading it from the catalog")
	}
	// The roster embeds keys and an address earlier steps computed, so its
	// block has to expand. Quoted, it would write the variable names.
	if !strings.Contains(script, "cat > quickstart-run/roster.json <<EOF\n") {
		t.Error("the roster block is not expanding; the keys and address would be written literally")
	}
	// The file that moves has nothing to expand, so it must not: an expanding
	// block is where a backtick or a dollar sign in content gets run or eaten.
	if !strings.Contains(script, "cat > quickstart-run/sample.csv <<'EOF'\n") {
		t.Error("the sample file block is expanding; its content is data and should stay literal")
	}
}

// A fence is at least three backticks. This document's prose opens lines with
// inline code spans, and one of those must stay prose rather than swallowing
// everything after it into the script.
func TestASingleBacktickDoesNotOpenABlock(t *testing.T) {
	t.Parallel()
	script := extractOrFail(t, "`dsops` builds it.\n\nrm -rf /\n")
	if strings.Contains(script, "rm -rf /") {
		t.Errorf("a line beginning with an inline code span opened a block:\n%s", script)
	}
}
