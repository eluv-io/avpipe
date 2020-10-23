Go Transport Stream parsing

TODO
* Removing use of TeeReader should give better performance 

SCTE-35 JSON subset used:
```json
{
   "pts": 8554888184,
   "protocol_version": 0,
   "tier": 4095,
   "encryption_algorithm": 0,
   "cw_index": 0,
   "splice_command_type": 6,
   "splice_null": {},
   "time_signal": {},
   "splice_descriptors": [
      {
         "splice_descriptor_tag": 2,
         "splice_descriptor_name": "segmentation_descriptor",
         "segmentation_event_id": 158662685,
         "segmentation_event_cancel_indicator": false,
         "segmentation_duration": 0,
         "segmentation_type_id": 33,
         "segment_num": 5,
         "segments_expected": 1,
         "sub_segment_num": 0,
         "sub_segments_expected": 0,
         "delivery_restrictions": {
            "web_delivery_allowed_flag": true,
            "no_regional_blackout_flag": true,
            "archive_allowed_flag": true,
            "device_restrictions": 3
         },
         "segmentation_upids": [
            {
               "segmentation_upid_type": 1,
               "format_identifier": 123,
               "upid": "EP034115060099 RVAwMzQxMTUwNjAwOTk="
            }
         ],
         "components": [
            {
               "component_tag": 8,
               "pts_offset": 33
            }
         ]
      },
      {
         "splice_descriptor_tag": 2,
         "...": "multiple descriptors allowed"
      }
   ]
}
```