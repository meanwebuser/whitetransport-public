package carriers

import (
	"sort"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// AvailabilityClass is an implementation classification, not a readiness
// claim. Live/provider readiness still requires a claim-specific canary.
type AvailabilityClass string

const (
	AvailabilityExecutable     AvailabilityClass = "executable"
	AvailabilityLocalFixture   AvailabilityClass = "local_fixture"
	AvailabilityPlannedBlocked AvailabilityClass = "planned_blocked"
)

// CapabilityInventoryRow keeps every known byte-moving medium visible even
// before its runtime adapter exists. CatalogIDs link product/catalog aliases to
// one canonical runtime row so new catalog entries cannot silently disappear.
type CapabilityInventoryRow struct {
	ID             string
	Source         string
	Availability   AvailabilityClass
	RuntimeID      string
	Construction   string
	CatalogIDs     []string
	FactoryAliases []string
	SourceSymbols  []string
	TrafficClasses []string
	Capabilities   []string
}

type inventoryClassification struct {
	availability   AvailabilityClass
	runtimeID      string
	construction   string
	catalogIDs     []string
	factoryAliases []string
	sourceSymbols  []string
}

// CapabilityInventory returns the closed, deterministic carrier inventory.
// Standard descriptor facts are generated from StandardDescriptors; rows that
// are not yet Go descriptors remain explicit planned-blocked obligations.
func CapabilityInventory() []CapabilityInventoryRow {
	standard := map[string]inventoryClassification{
		CarrierVKMessages:    {availability: AvailabilityExecutable, runtimeID: CarrierVKMessages, construction: "native", catalogIDs: []string{"vk-text", "vk-text-2t"}, factoryAliases: []string{"vk"}, sourceSymbols: []string{"VKChannel", "VKProvider", "VKMultiTokenProvider", "VKWebhookProvider"}},
		CarrierOKMessages:    {availability: AvailabilityExecutable, runtimeID: CarrierOKMessages, construction: "native", catalogIDs: []string{"ok-text"}, factoryAliases: []string{"ok"}, sourceSymbols: []string{"OKChannel", "OKProvider", "OKWebhookProvider"}},
		CarrierWBStreamVP8:   {availability: AvailabilityExecutable, runtimeID: "wbstream", construction: "provider_registry"},
		CarrierVKDocs256:     {availability: AvailabilityExecutable, runtimeID: CarrierVKDocs256, construction: "native", catalogIDs: []string{"vk-doc-256"}, sourceSymbols: []string{"VKDocumentProvider"}},
		CarrierVKDocs1024:    {availability: AvailabilityExecutable, runtimeID: CarrierVKDocs1024, construction: "native", catalogIDs: []string{"vk-doc-1024"}},
		CarrierVKPhotos:      {availability: AvailabilityPlannedBlocked, catalogIDs: []string{"vk-photo"}, sourceSymbols: []string{"VKPhotoProvider"}},
		CarrierOKDocs256:     {availability: AvailabilityExecutable, runtimeID: CarrierOKDocs256, construction: "native", catalogIDs: []string{"ok-doc-256"}, sourceSymbols: []string{"OKDocumentProvider"}},
		CarrierOKPhotos:      {availability: AvailabilityPlannedBlocked, catalogIDs: []string{"ok-photo"}, sourceSymbols: []string{"OKPhotoProvider"}},
		CarrierYandexDisk:    {availability: AvailabilityExecutable, runtimeID: CarrierYandexDisk, construction: "native", catalogIDs: []string{"yandex-disk"}, sourceSymbols: []string{"YandexDiskChannel", "YandexDiskProvider"}},
		CarrierGitRepository: {availability: AvailabilityExecutable, runtimeID: CarrierGitRepository, construction: "native"},
		CarrierMailIMAPSMTP:  {availability: AvailabilityExecutable, runtimeID: CarrierMailIMAPSMTP, construction: "native"},
		CarrierSSHTCP:        {availability: AvailabilityExecutable, runtimeID: CarrierSSHTCP, construction: "native"},
		CarrierSSHFabric:     {availability: AvailabilityExecutable, runtimeID: CarrierSSHFabric, construction: "native"},
		CarrierSingBoxVLESS:  {availability: AvailabilityExecutable, runtimeID: CarrierSingBoxVLESS, construction: "native"},
	}

	rows := make([]CapabilityInventoryRow, 0, len(standard)+14)
	for _, descriptor := range StandardDescriptors() {
		classification := standard[descriptor.ID]
		rows = append(rows, CapabilityInventoryRow{
			ID:             descriptor.ID,
			Source:         "standard_descriptor",
			Availability:   classification.availability,
			RuntimeID:      classification.runtimeID,
			Construction:   classification.construction,
			CatalogIDs:     cloneStrings(classification.catalogIDs),
			FactoryAliases: cloneStrings(classification.factoryAliases),
			SourceSymbols:  cloneStrings(classification.sourceSymbols),
			TrafficClasses: trafficClassStrings(descriptor.TrafficClasses),
			Capabilities:   capabilityStrings(descriptor.Capabilities),
		})
	}

	fileDescriptor, _ := FindStandardDescriptor(CarrierFileMailbox)
	rows = append(rows, CapabilityInventoryRow{
		ID:             CarrierFileMailbox,
		Source:         "local_fixture",
		Availability:   AvailabilityLocalFixture,
		RuntimeID:      CarrierFileMailbox,
		Construction:   "native",
		SourceSymbols:  []string{"FileProvider"},
		TrafficClasses: trafficClassStrings(fileDescriptor.TrafficClasses),
		Capabilities:   capabilityStrings(fileDescriptor.Capabilities),
	})

	rows = append(rows,
		registryRow("wbstream", "wbstream", []string{"wbstream"}, nil, []string{"wbstream"}),
		registryRow("telemost", "telemost", []string{"telemost", "telemost-video-vp8"}, nil, []string{"telemost"}),
		registryRow("dion", "dion", nil, nil, []string{"dion"}),
		registryRow(CarrierDIONCall, "dion", nil, nil, nil),
		registryRow("vkcall", "vkcall", []string{"vk", "vk-video-datachannel", "vk-video-vp8", "vk-video-dualstream"}, nil, []string{"vkcall"}),
		plannedRow("telemost.video.dualstream", []string{"telemost-video-dualstream"}, nil),
		plannedRow("telemost.video.datachannel", []string{"telemost/datachannel"}, nil),
		plannedRow("vk.audio.future", []string{"vk/future-audio"}, nil),
		plannedRow("telemost.audio.future", []string{"telemost/future-audio"}, nil),
		plannedRow("vk.browser.bridge", []string{"vk-browser-bridge"}, []string{"VKBrowserBridgeProvider"}),
		plannedRow("telegram.messages", []string{"tg-bot", "tg-2bots"}, []string{"TGChannel", "TelegramProvider", "TGWebhookProvider"}),
		plannedRow("mailru.cloud.files", nil, []string{"MailRuCloudChannel"}),
		plannedRow("sber.cloud.files", nil, []string{"SberCloudChannel"}),
		plannedRow("audio.fsk", nil, []string{"AudioEncoder"}),
		CapabilityInventoryRow{
			ID:             "memory.provider",
			Source:         "local_fixture",
			Availability:   AvailabilityLocalFixture,
			RuntimeID:      "memory",
			Construction:   "provider_registry",
			FactoryAliases: []string{"memory"},
			SourceSymbols:  []string{"MemoryProvider"},
		},
	)

	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func registryRow(id, runtimeID string, catalogIDs, sourceSymbols, factoryAliases []string) CapabilityInventoryRow {
	return CapabilityInventoryRow{
		ID:             id,
		Source:         "config_alias",
		Availability:   AvailabilityExecutable,
		RuntimeID:      runtimeID,
		Construction:   "provider_registry",
		CatalogIDs:     cloneStrings(catalogIDs),
		FactoryAliases: cloneStrings(factoryAliases),
		SourceSymbols:  cloneStrings(sourceSymbols),
		TrafficClasses: []string{"egress", "stream"},
		Capabilities:   []string{"stream"},
	}
}

func plannedRow(id string, catalogIDs, sourceSymbols []string) CapabilityInventoryRow {
	return CapabilityInventoryRow{
		ID:            id,
		Source:        "known_medium",
		Availability:  AvailabilityPlannedBlocked,
		CatalogIDs:    cloneStrings(catalogIDs),
		SourceSymbols: cloneStrings(sourceSymbols),
	}
}

func trafficClassStrings(classes []fabric.TrafficClass) []string {
	out := make([]string, len(classes))
	for index, class := range classes {
		out[index] = string(class)
	}
	return out
}

func capabilityStrings(capabilities []Capability) []string {
	out := make([]string, len(capabilities))
	for index, capability := range capabilities {
		out[index] = string(capability)
	}
	return out
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}
