package legacy

import (
	"strings"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

func TestLoadMapsLegacyGroups(t *testing.T) {
	input := `{
	  "homebrew": {
	    "install_once": ["homebrew/cask/example-app"],
	    "always_update": [{"name":"vendor/tools/example-cli","type":"formula"}]
	  },
	  "mac_app_store": {
	    "apps": [{"id":"123456789","name":"Example Store App"}]
	  },
	  "bun": {
	    "global_packages": ["example-language-server"]
	  },
	  "custom_installers": {
	    "apps": [{
	      "name":"example-custom",
	      "check_command":"command -v example-custom",
	      "install_command":"example-installer"
	    }]
	  }
	}`

	got, err := Load(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.Packages) != 4 {
		t.Fatalf("Load() packages = %d, want 4", len(got.Packages))
	}
	if got.Packages[0].Kind != model.KindCask ||
		got.Packages[0].UpdatePolicy != model.UpdateInstallOnly {
		t.Fatalf("install-once package = %#v", got.Packages[0])
	}
	if got.Packages[1].Source != "vendor/tools" ||
		got.Packages[1].Kind != model.KindFormula {
		t.Fatalf("managed package = %#v", got.Packages[1])
	}
	if len(got.CustomInstallers) != 1 || !got.CustomInstallers[0].ReviewRequired {
		t.Fatalf("custom installers = %#v", got.CustomInstallers)
	}
	if len(got.Warnings) == 0 {
		t.Fatal("Load() did not warn about legacy commands")
	}
}
