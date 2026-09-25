// Command arncheck verifies that every mapped action can actually authorize
// against the resource type its entry's arn_format names.
//
// It reads AWS's machine-readable service reference, which needs no credentials
// and creates nothing. It is separate from the preflight binary because it is a
// maintainer tool, and separate from `go test` because it makes network calls
// and the test suite must run offline.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/Caleb-Kelly-25/preflight/internal/awsref"
	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
	"github.com/Caleb-Kelly-25/preflight/mappings"
)

func main() {
	strict := flag.Bool("strict", false,
		"exit non-zero when a service reference could not be fetched, instead of only on a real mismatch")
	timeout := flag.Duration("timeout", 30*time.Second, "per-request timeout")
	flag.Parse()

	db, err := mapping.Load(mappings.FS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading mapping database: %v\n", err)
		os.Exit(2)
	}

	client := &http.Client{Timeout: *timeout}
	var fetchFailed bool
	fetch := func(service string) (*awsref.Service, error) {
		resp, err := client.Get(awsref.URL(service))
		if err != nil {
			fetchFailed = true
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fetchFailed = true
			return nil, fmt.Errorf("%s returned %s", awsref.URL(service), resp.Status)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			fetchFailed = true
			return nil, err
		}
		return awsref.Parse(newReader(body))
	}

	findings := awsref.Check(db, fetch)

	var errors, warns int
	for _, f := range findings {
		fmt.Println(f)
		switch f.Severity {
		case awsref.Error:
			errors++
		case awsref.Warn:
			warns++
		}
	}
	fmt.Printf("\n%d error(s), %d warning(s), %d note(s)\n",
		errors, warns, len(findings)-errors-warns)

	// A mismatch is a real defect and fails. A fetch failure is not: this reads
	// a live third-party endpoint, and a check that reddens the build because
	// AWS had an outage is a check people learn to route around. --strict is
	// there for anyone who would rather know.
	if errors > 0 {
		os.Exit(1)
	}
	if fetchFailed {
		fmt.Fprintln(os.Stderr,
			"warning: at least one service reference could not be fetched; those actions were NOT checked")
		if *strict {
			os.Exit(1)
		}
	}
	os.Exit(0)
}

// newReader avoids pulling bytes.NewReader into the import list for one use.
func newReader(b []byte) io.Reader { return &sliceReader{b: b} }

type sliceReader struct {
	b []byte
	i int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
