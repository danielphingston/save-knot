package registrybackup

import "testing"

func TestValidateDocumentScopesPathsToConfiguredRoots(t *testing.T) {
	document := Document{Version: 1, Keys: []KeyData{{Path: `hkey_current_user/Software/Vendor/Game/Child`}}}
	if err := validateDocument(document, []string{`HKCU\software\vendor\game`}); err != nil {
		t.Fatalf("case-insensitive child path rejected: %v", err)
	}

	for _, path := range []string{
		`HKCU\Software\Vendor\GameOther`,
		`HKLM\Software\Vendor\Game\Child`,
		`HKCU\Software\Vendor\Game\..\Other`,
	} {
		t.Run(path, func(t *testing.T) {
			invalid := Document{Version: 1, Keys: []KeyData{{Path: path}}}
			if err := validateDocument(invalid, []string{`HKCU\Software\Vendor\Game`}); err == nil {
				t.Fatalf("out-of-scope path %q was accepted", path)
			}
		})
	}
}

func TestValidateDocumentRejectsDuplicateRegistryPathsAndValues(t *testing.T) {
	document := Document{Version: 1, Keys: []KeyData{
		{Path: `HKCU\Software\Vendor\Game`, Values: []ValueData{{Name: "Setting"}, {Name: "setting"}}},
	}}
	if err := validateDocument(document, []string{`HKCU\Software\Vendor\Game`}); err == nil {
		t.Fatal("duplicate case-insensitive value names were accepted")
	}

	document = Document{Version: 1, Keys: []KeyData{
		{Path: `HKCU\Software\Vendor\Game`},
		{Path: `HKEY_CURRENT_USER/software/vendor/game`},
	}}
	if err := validateDocument(document, []string{`HKCU\Software\Vendor\Game`}); err == nil {
		t.Fatal("duplicate case-insensitive key paths were accepted")
	}
}
