package ts

import (
	"fmt"

	"github.com/Comcast/gots/scte35"
)

// The purpose of this struct is for JSON marshaling with minimal complexity
type SpliceInfo struct {
	PID                 uint16                   `json:"pid"`
	PTS                 uint64                   `json:"pts"`
	ProtocolVersion     uint8                    `json:"protocol_version,omitempty"` // always 0
	Tier                uint16                   `json:"tier"`
	EncryptionAlgorithm uint8                    `json:"encryption_algorithm,omitempty"` // not handled
	CWIndex             uint8                    `json:"cw_index,omitempty"`             // not handled
	SpliceCommandType   scte35.SpliceCommandType `json:"splice_command_type"`
	SpliceDescriptors   []SpliceDescriptor       `json:"splice_descriptors,omitempty"`
	//SpliceNull          *SpliceNull              `json:"splice_null,omitempty"`
	//TimeSignal          *TimeSignal              `json:"time_signal,omitempty"`
}

// unnecessary for now
//type SpliceNull struct {
//}
//
//type TimeSignal struct {
//}

type SpliceDescriptor struct {
	SpliceDescriptorTag uint8 `json:"splice_descriptor_tag"`
	//SpliceDescriptorName string `json:"splice_descriptor_name"`

	// SegmentationDescriptor fields
	//
	// scte35.SegDescType seems to be missing:
	//   OpeningCreditStart                                   = 36
	//   OpeningCreditEnd                                     = 37
	//   ClosingCreditStart                                   = 38
	//   ClosingCreditEnd                                     = 39
	//   ProviderOverlayPlacementOpportunityStart             = 56
	//   ProviderOverlayPlacementOpportunityEnd               = 57
	//   DistributorOverlayPlacementOpportunityStart          = 58
	//   DistributorOverlayPlacementOpportunityEnd            = 59
	SegmentationEventId              uint32                `json:"segmentation_event_id"`
	SegmentationEventCancelIndicator bool                  `json:"segmentation_event_cancel_indicator"`
	SegmentationDuration             uint64                `json:"segmentation_duration,omitempty"`
	SegmentationTypeId               scte35.SegDescType    `json:"segmentation_type_id"`
	SegmentNum                       uint8                 `json:"segment_num"`
	SegmentsExpected                 uint8                 `json:"segments_expected"`
	SubSegmentNum                    uint8                 `json:"sub_segment_num"`
	SubSegmentsExpected              uint8                 `json:"sub_segments_expected"`
	DeliveryRestrictions             *DeliveryRestrictions `json:"delivery_restrictions,omitempty"`
	SegmentationUpids                []SegmentationUpid    `json:"segmentation_upids,omitempty"`
	Components                       []Component           `json:"components,omitempty"`

	// Not implemnted: AvailDescriptor, DTMFDescriptor, TimeDescriptor, AudioDescriptor
}

type DeliveryRestrictions struct {
	WebDeliveryAllowed bool                      `json:"web_delivery_allowed_flag"`
	NoRegionalBlackout bool                      `json:"no_regional_blackout_flag"`
	ArchiveAllowed     bool                      `json:"archive_allowed_flag"`
	DeviceRestrictions scte35.DeviceRestrictions `json:"device_restrictions"`
}

type SegmentationUpid struct {
	SegmentationUpidType scte35.SegUPIDType `json:"segmentation_upid_type"`
	FormatIdentifier     uint32             `json:"format_identifier,omitempty"` // not handled
	Upid                 []byte             `json:"upid,omitempty"`
}

type Component struct {
	ComponentTag uint8  `json:"component_tag"`
	PTSOffset    uint64 `json:"pts_offset"`
}

// TODO We should parse the SCTE data directly instead of going through layers of gots, which doesn't support JSON marshaling
func ConvertGots(pid uint16, gsi scte35.SCTE35) (si SpliceInfo, err error) {
	si = SpliceInfo{
		PID:               pid,
		SpliceDescriptors: []SpliceDescriptor{},
	}
	si.PTS = uint64(gsi.PTS())
	si.Tier = gsi.Tier()
	si.SpliceCommandType = gsi.Command()
	switch si.SpliceCommandType {
	case scte35.SpliceNull:
		//si.SpliceNull = &SpliceNull{}
	case scte35.TimeSignal:
		//si.TimeSignal = &TimeSignal{}
	default:
		err = fmt.Errorf("SpliceCommandType %d not handled", si.SpliceCommandType)
	}
	for _, gdesc := range gsi.Descriptors() {
		desc := SpliceDescriptor{
			//SpliceDescriptorName:           "segmentation_descriptor",
			SpliceDescriptorTag:              2, // gots only parses SegmentationDescriptors
			SegmentationEventId:              gdesc.EventID(),
			SegmentationEventCancelIndicator: gdesc.IsEventCanceled(),
			SegmentationDuration:             uint64(gdesc.Duration()),
			SegmentationTypeId:               gdesc.TypeID(),
			SegmentNum:                       gdesc.SegmentNum(),
			SegmentsExpected:                 gdesc.SegmentsExpected(),
			SubSegmentNum:                    gdesc.SubSegmentNumber(),
			SubSegmentsExpected:              gdesc.SubSegmentsExpected(),
			SegmentationUpids:                []SegmentationUpid{},
			Components:                       []Component{},
		}
		if !gdesc.IsDeliveryNotRestricted() {
			desc.DeliveryRestrictions = &DeliveryRestrictions{
				WebDeliveryAllowed: gdesc.IsWebDeliveryAllowed(),
				NoRegionalBlackout: gdesc.HasNoRegionalBlackout(),
				ArchiveAllowed:     gdesc.IsArchiveAllowed(),
				DeviceRestrictions: gdesc.DeviceRestrictions(),
			}
		}
		if gdesc.UPIDType() == scte35.SegUPIDMID {
			for _, gupid := range gdesc.MID() {
				upid := make([]byte, len(gupid.UPID()))
				copy(upid, gupid.UPID())
				desc.SegmentationUpids = append(desc.SegmentationUpids, SegmentationUpid{
					SegmentationUpidType: gupid.UPIDType(),
					FormatIdentifier:     0,
					Upid:                 gupid.UPID(),
				})
			}
		} else {
			upid := make([]byte, len(gdesc.UPID()))
			copy(upid, gdesc.UPID())
			desc.SegmentationUpids = append(desc.SegmentationUpids, SegmentationUpid{
				SegmentationUpidType: gdesc.UPIDType(),
				FormatIdentifier:     0,
				Upid:                 upid,
			})
		}
		for _, gc := range gdesc.Components() {
			desc.Components = append(desc.Components, Component{
				ComponentTag: gc.ComponentTag(),
				PTSOffset:    uint64(gc.PTSOffset()),
			})
		}
		si.SpliceDescriptors = append(si.SpliceDescriptors, desc)
	}

	return si, err
}
