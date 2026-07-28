package mas

import (
	"context"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

type fakeRunner struct {
	output []byte
	err    error
}

func (r fakeRunner) Output(context.Context, string, ...string) ([]byte, error) {
	return r.output, r.err
}

func TestCollectsNDJSONRecords(t *testing.T) {
	input := []byte(
		"{\"adamID\":123456789,\"displayName\":\"Example App\",\"path\":\"/Applications/Example App.app\",\"version\":\"2.3\"}\n" +
			"{\"adamID\":987654321,\"displayName\":\"Another App\",\"path\":\"/Applications/Another App.app\",\"version\":\"4.5\"}\n",
	)

	got, err := NewCollector(fakeRunner{output: input}).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Collect() returned %d packages, want 2", len(got))
	}
	if got[0].Manager != model.ManagerMAS ||
		got[0].Package != "123456789" ||
		got[0].Application != "/Applications/Example App.app" {
		t.Fatalf("first package = %#v", got[0])
	}
}

func TestRejectsInvalidRecord(t *testing.T) {
	_, err := NewCollector(fakeRunner{output: []byte("{not-json}\n")}).Collect(context.Background())
	if err == nil {
		t.Fatal("Collect() error = nil, want error")
	}
}

func TestIgnoresRecordsWithoutStoreIdentity(t *testing.T) {
	input := []byte(
		"{\"adamID\":0,\"displayName\":\"Unidentified App\",\"path\":\"/Applications/Unidentified.app\",\"version\":\"1.0\"}\n",
	)
	got, err := NewCollector(fakeRunner{output: input}).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Collect() returned %#v, want empty", got)
	}
}
