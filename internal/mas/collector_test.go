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

type fallbackRunner struct {
	json  []byte
	plain []byte
}

func (r fallbackRunner) Output(
	_ context.Context,
	_ string,
	args ...string,
) ([]byte, error) {
	if len(args) == 2 && args[0] == "list" && args[1] == "--json" {
		return r.json, nil
	}
	return r.plain, nil
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

func TestPlaceholderRecordsFallBackToPlainInventory(t *testing.T) {
	runner := fallbackRunner{
		json: []byte(
			"{\"name\":\"\"}\n" +
				"{\"adamID\":1511935951,\"displayName\":\"BetterJSON\",\"path\":\"/Applications/BetterJSON.app\",\"version\":\"2.3\"}\n",
		),
		plain: []byte(
			"1511935951  BetterJSON       (2.3)\n" +
				"1518036000  Sequel Ace       (5.3.0)\n" +
				" 803453959  Slack            (4.51.180)\n",
		),
	}
	got, err := NewCollector(runner).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 ||
		got[0].Package != "1511935951" ||
		got[0].Application != "/Applications/BetterJSON.app" ||
		got[1].Package != "1518036000" ||
		got[2].Package != "803453959" {
		t.Fatalf("packages = %#v", got)
	}
}

func TestPlaceholderFallbackMustProduceIdentities(t *testing.T) {
	_, err := NewCollector(fallbackRunner{
		json: []byte("{\"name\":\"\"}\n"),
	}).Collect(context.Background())
	if err == nil {
		t.Fatal("Collect() error = nil, want incomplete-inventory failure")
	}
}
