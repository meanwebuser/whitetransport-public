package carriers

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestCapabilityInventoryHasNoUnclassifiedStandardOrMandatoryMedium(t *testing.T) {
	rows := CapabilityInventory()
	byID := make(map[string]CapabilityInventoryRow, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.ID) == "" {
			t.Fatal("inventory contains an empty ID")
		}
		if _, duplicate := byID[row.ID]; duplicate {
			t.Fatalf("duplicate inventory ID %q", row.ID)
		}
		if row.Availability != AvailabilityExecutable && row.Availability != AvailabilityLocalFixture && row.Availability != AvailabilityPlannedBlocked {
			t.Fatalf("inventory row %s has unclassified availability %q", row.ID, row.Availability)
		}
		if row.Availability != AvailabilityPlannedBlocked && strings.TrimSpace(row.RuntimeID) == "" {
			t.Fatalf("available row %s has no runtime binding", row.ID)
		}
		if row.Availability == AvailabilityExecutable && !containsTrafficClass(row.TrafficClasses, "egress") {
			t.Fatalf("executable byte medium %s is not classified for Internet egress", row.ID)
		}
		byID[row.ID] = row
	}

	for _, descriptor := range StandardDescriptors() {
		row, ok := byID[descriptor.ID]
		if !ok {
			t.Errorf("standard descriptor %s disappeared from capability inventory", descriptor.ID)
			continue
		}
		if len(row.TrafficClasses) == 0 || len(row.Capabilities) == 0 {
			t.Errorf("standard descriptor %s lost traffic/capability facts", descriptor.ID)
		}
	}

	mandatory := map[string]AvailabilityClass{
		CarrierVKPhotos:      AvailabilityPlannedBlocked,
		CarrierOKPhotos:      AvailabilityPlannedBlocked,
		CarrierFileMailbox:   AvailabilityLocalFixture,
		CarrierMailIMAPSMTP:  AvailabilityExecutable,
		"git.repository":     AvailabilityExecutable,
		"telegram.messages":  AvailabilityPlannedBlocked,
		"mailru.cloud.files": AvailabilityPlannedBlocked,
		"sber.cloud.files":   AvailabilityPlannedBlocked,
		"audio.fsk":          AvailabilityPlannedBlocked,
		"wbstream":           AvailabilityExecutable,
		"telemost":           AvailabilityExecutable,
		"dion":               AvailabilityExecutable,
		"vkcall":             AvailabilityExecutable,
	}
	for id, want := range mandatory {
		row, ok := byID[id]
		if !ok {
			t.Errorf("mandatory byte medium %s is omitted", id)
			continue
		}
		if row.Availability != want {
			t.Errorf("mandatory byte medium %s availability=%s want=%s", id, row.Availability, want)
		}
	}
}

func containsTrafficClass(classes []string, want string) bool {
	for _, class := range classes {
		if class == want {
			return true
		}
	}
	return false
}

func TestCapabilityInventoryClassifiesEveryTypeScriptCarrierCatalogRow(t *testing.T) {
	aliases := make(map[string]string)
	for _, row := range CapabilityInventory() {
		for _, catalogID := range row.CatalogIDs {
			if previous := aliases[catalogID]; previous != "" {
				t.Fatalf("catalog ID %s mapped by both %s and %s", catalogID, previous, row.ID)
			}
			aliases[catalogID] = row.ID
		}
	}

	source := readProviderCatalogSource(t)
	ids := append(
		catalogIDsInSection(t, source, "export const YTP_PROVIDER_CATALOG", "  strategies:"),
		catalogIDsInSection(t, source, "export const VIDEO_CONFERENCE_PROVIDER_CATALOG", "  unsupportedCombinations:")...,
	)
	ids = append(ids, catalogIDsInSection(t, source, "export const WB_PLATFORM_CATALOG", "  resourceModes:")...)
	unsupported := sectionBetween(t, source, "  unsupportedCombinations:", "};")
	ids = append(ids, unsupportedCombinationIDs(unsupported)...)
	var missing []string
	for _, id := range ids {
		if aliases[id] == "" {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("TypeScript byte-media catalog rows are unclassified: %s", strings.Join(missing, ", "))
	}
}

func TestCapabilityInventoryClassifiesEveryExportedTypeScriptChannel(t *testing.T) {
	symbols := make(map[string]string)
	for _, row := range CapabilityInventory() {
		for _, symbol := range row.SourceSymbols {
			if previous := symbols[symbol]; previous != "" {
				t.Fatalf("source symbol %s mapped by both %s and %s", symbol, previous, row.ID)
			}
			symbols[symbol] = row.ID
		}
	}

	indexSource := readRepositoryFile(t, "packages/any-transport/packages/providers/index.ts")
	exportedChannels := exportedChannelSymbols(indexSource)
	paths, err := filepath.Glob(repositoryPath(t, "packages/any-transport/packages/providers/channel-*.ts"))
	if err != nil {
		t.Fatalf("glob TypeScript channels: %v", err)
	}
	var missing []string
	for _, path := range paths {
		base := filepath.Base(path)
		if strings.HasPrefix(base, "channel-contract") {
			continue
		}
		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read TypeScript channel %s: %v", path, readErr)
		}
		matches := regexp.MustCompile(`(?m)^export class ([A-Za-z][A-Za-z0-9]*Channel)\b`).FindAllStringSubmatch(string(payload), -1)
		if len(matches) == 0 {
			t.Errorf("byte-medium channel source %s exports no *Channel class", base)
		}
		for _, match := range matches {
			symbol := match[1]
			if symbols[symbol] == "" {
				missing = append(missing, symbol)
			}
			if !exportedChannels[symbol] {
				t.Errorf("TypeScript channel %s is not exported from provider index", symbol)
			}
		}
	}
	for symbol := range exportedChannels {
		if symbols[symbol] == "" {
			missing = append(missing, symbol)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("exported TypeScript transport channels are unclassified: %s", strings.Join(missing, ", "))
	}
	if symbols["AudioEncoder"] == "" {
		t.Fatal("audio byte medium is omitted from the inventory")
	}

	legacySection := sectionBetween(t, indexSource, "// ── Legacy providers", "export type {")
	providers := regexp.MustCompile(`\b([A-Z][A-Za-z0-9]*Provider)\b`).FindAllStringSubmatch(legacySection, -1)
	missing = missing[:0]
	for _, match := range providers {
		if symbols[match[1]] == "" {
			missing = append(missing, match[1])
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("exported legacy TypeScript providers are unclassified: %s", strings.Join(missing, ", "))
	}
}

func TestCapabilityInventoryClassifiesEveryConfiguredAdapterRegistryAlias(t *testing.T) {
	byID := make(map[string]CapabilityInventoryRow)
	factoryRows := make(map[string]string)
	for _, row := range CapabilityInventory() {
		byID[row.ID] = row
		for _, alias := range row.FactoryAliases {
			if previous := factoryRows[alias]; previous != "" {
				t.Fatalf("runtime factory %s mapped by both %s and %s", alias, previous, row.ID)
			}
			factoryRows[alias] = row.ID
		}
	}

	configSource := readRepositoryFile(t, "core/go/internal/config/config.go")
	configSection := sectionBetween(t, configSource, "var adapterRegistryNames", "// enabledCarrierRuntimeID")
	configured := regexp.MustCompile(`"([^"]+)"\s*:\s*true`).FindAllStringSubmatch(configSection, -1)

	runtimeSource := readRepositoryFile(t, "core/go/internal/runtime/adapters.go")
	registered := make(map[string]bool)
	for _, match := range regexp.MustCompile(`r\.factories\["([^"]+)"\]`).FindAllStringSubmatch(runtimeSource, -1) {
		registered[match[1]] = true
	}
	registeredIDs := make([]string, 0, len(registered))
	for id := range registered {
		registeredIDs = append(registeredIDs, id)
	}
	if missing := unmappedIDs(registeredIDs, factoryRows); len(missing) > 0 {
		t.Fatalf("runtime builtin factories are unclassified: %s", strings.Join(missing, ", "))
	}
	for _, match := range configured {
		id := match[1]
		row, ok := byID[id]
		if !ok || row.Availability != AvailabilityExecutable || row.Construction != "provider_registry" {
			t.Errorf("configured adapter alias %s is not explicitly executable through provider_registry", id)
		}
		if !registered[id] {
			t.Errorf("configured adapter alias %s has no runtime builtin factory", id)
		}
		if factoryRows[id] != row.ID {
			t.Errorf("configured adapter alias %s factory maps to %s, want row %s", id, factoryRows[id], row.ID)
		}
	}
}

func TestInventoryCoverageRejectsUndeclaredRuntimeFactoryAndExportedProvider(t *testing.T) {
	tests := []struct {
		name       string
		discovered []string
		mapping    map[string]string
		want       string
	}{
		{name: "runtime factory", discovered: []string{"known", "new-runtime-factory"}, mapping: map[string]string{"known": "canonical"}, want: "new-runtime-factory"},
		{name: "exported provider", discovered: []string{"KnownProvider", "NewByteProvider"}, mapping: map[string]string{"KnownProvider": "canonical"}, want: "NewByteProvider"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			missing := unmappedIDs(test.discovered, test.mapping)
			if len(missing) != 1 || missing[0] != test.want {
				t.Fatalf("undeclared source row was not rejected: %v", missing)
			}
		})
	}
}

func TestInventoryLexicalExtractionAcceptsQuoteAndFieldOrderVariants(t *testing.T) {
	source := `export const TEST = { rows: [{ id: "double-quoted" }, { id: 'single-quoted' }], end: true }`
	ids := catalogIDsInSection(t, source, "export const TEST", "end: true")
	if strings.Join(ids, ",") != "double-quoted,single-quoted" {
		t.Fatalf("quote-independent IDs = %v", ids)
	}
	unsupported := `[{ mode: "future-audio", reason: "x", platform: "vk" }, { platform: 'telemost', reason: 'y', mode: 'datachannel' }]`
	combos := unsupportedCombinationIDs(unsupported)
	if strings.Join(combos, ",") != "vk/future-audio,telemost/datachannel" {
		t.Fatalf("order-independent unsupported combinations = %v", combos)
	}
	exports := exportedChannelSymbols("export {\n  MultiLineChannel,\n} from \"./channel-new\";")
	if !exports["MultiLineChannel"] {
		t.Fatal("multiline/double-quoted channel export was not discovered")
	}
}

func unmappedIDs(discovered []string, mapping map[string]string) []string {
	var missing []string
	for _, id := range discovered {
		if mapping[id] == "" {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return missing
}

func readProviderCatalogSource(t *testing.T) string {
	t.Helper()
	return readRepositoryFile(t, "packages/provider-channels/src/provider-catalogs.ts")
}

func readRepositoryFile(t *testing.T, relativePath string) string {
	t.Helper()
	path := repositoryPath(t, relativePath)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repository file %s: %v", path, err)
	}
	return string(payload)
}

func repositoryPath(t *testing.T, relativePath string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve inventory test path")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../../..", relativePath))
	return path
}

func catalogIDsInSection(t *testing.T, source, startMarker, endMarker string) []string {
	t.Helper()
	section := sectionBetween(t, source, startMarker, endMarker)
	matches := regexp.MustCompile(`(?m)\bid\s*:\s*["']([^"']+)["']`).FindAllStringSubmatch(section, -1)
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		ids = append(ids, match[1])
	}
	return ids
}

func unsupportedCombinationIDs(section string) []string {
	var ids []string
	objects := regexp.MustCompile(`(?s)\{([^{}]*)\}`).FindAllStringSubmatch(section, -1)
	for _, object := range objects {
		platform := literalField(object[1], "platform")
		mode := literalField(object[1], "mode")
		if platform != "" && mode != "" {
			ids = append(ids, platform+"/"+mode)
		}
	}
	return ids
}

func literalField(object, field string) string {
	pattern := regexp.MustCompile(`(?m)\b` + regexp.QuoteMeta(field) + `\s*:\s*["']([^"']+)["']`)
	match := pattern.FindStringSubmatch(object)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func exportedChannelSymbols(source string) map[string]bool {
	out := make(map[string]bool)
	blocks := regexp.MustCompile(`(?s)export\s*\{([^}]*)\}\s*from\s*["']\./channel-[^"']+["']\s*;`).FindAllStringSubmatch(source, -1)
	identifier := regexp.MustCompile(`\b([A-Z][A-Za-z0-9]*Channel)\b`)
	for _, block := range blocks {
		for _, match := range identifier.FindAllStringSubmatch(block[1], -1) {
			out[match[1]] = true
		}
	}
	return out
}

func sectionBetween(t *testing.T, source, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(source, startMarker)
	if start < 0 {
		t.Fatalf("catalog start marker %q missing", startMarker)
	}
	endOffset := strings.Index(source[start:], endMarker)
	if endOffset < 0 {
		t.Fatalf("catalog end marker %q missing after %q", endMarker, startMarker)
	}
	return source[start : start+endOffset]
}
