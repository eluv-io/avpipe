package cmd

import (
	"fmt"

	"github.com/qluvio/avpipe"
	"github.com/spf13/cobra"
)

func Probe(cmdRoot *cobra.Command) error {
	cmdProbe := &cobra.Command{
		Use:   "probe",
		Short: "Probe a media file",
		Long:  "Probe a media file and print the result",
		RunE:  doProbe,
	}

	cmdRoot.AddCommand(cmdProbe)

	cmdProbe.PersistentFlags().StringP("filename", "f", "", "(mandatory) filename to be probed")
	cmdProbe.PersistentFlags().BoolP("seekable", "", false, "(optional) seekable stream")

	return nil
}

func doProbe(cmd *cobra.Command, args []string) error {

	filename := cmd.Flag("filename").Value.String()
	if len(filename) == 0 {
		return fmt.Errorf("Filename is needed after -f")
	}

	seekable, err := cmd.Flags().GetBool("seekable")
	if err != nil {
		return fmt.Errorf("Invalid seekable flag")
	}
	log.Debug("doProbe", "seekable", seekable, "filename", filename)

	avpipe.InitIOHandler(&avcmdInputOpener{url: filename}, &avcmdOutputOpener{dir: ""})

	probe, err := avpipe.Probe(filename, seekable)
	if err != nil {
		return fmt.Errorf("Probing failed. file=%s", filename)
	}

	for i, info := range probe.StreamInfo {
		channelLayoutName := "-"
		if info.CodecType == "audio" {
			channelLayoutName = avpipe.ChannelLayoutName(info.Channels, info.ChannelLayout)
		}

		fmt.Printf("Stream[%d]\n", i)
		fmt.Printf("\tcodec_type: %s\n", info.CodecType)
		fmt.Printf("\tcodec_id: %d\n", info.CodecID)
		fmt.Printf("\tcodec_name: %s\n", info.CodecName)
		fmt.Printf("\tduration_ts: %d\n", info.DurationTs)
		fmt.Printf("\ttime_base: %d/%d\n", info.TimeBase.Num(), info.TimeBase.Denom())
		fmt.Printf("\tnb_frames: %d\n", info.NBFrames)
		fmt.Printf("\tstart_time: %d\n", info.StartTime)
		fmt.Printf("\tavg_frame_rate: %d/%d\n", info.AvgFrameRate.Num(), info.AvgFrameRate.Denom())
		fmt.Printf("\tframe_rate: %d/%d\n", info.FrameRate.Num(), info.FrameRate.Denom())
		fmt.Printf("\tSampleRate: %d\n", info.SampleRate)
		fmt.Printf("\tchannels: %d\n", info.Channels)
		fmt.Printf("\tchannel_layout: %s\n", channelLayoutName)
		fmt.Printf("\tticks_per_frame: %d\n", info.TicksPerFrame)
		fmt.Printf("\tbit_rate: %d\n", info.BitRate)
		fmt.Printf("\thas_b_frames: %v\n", info.Has_B_Frames)
		fmt.Printf("\twidth: %d\n", info.Width)
		fmt.Printf("\theight: %d\n", info.Height)
		if info.PixFmt >= 0 {
			fmt.Printf("\tpix_fmt: %d\n", info.PixFmt)
		} else {
			fmt.Printf("\tpix_fmt: -\n")
		}
		fmt.Printf("\tsample_aspect_ratio: %d:%d\n", info.SampleAspectRatio.Num(), info.SampleAspectRatio.Denom())
		fmt.Printf("\tdisplay_aspect_ratio: %d:%d\n", info.DisplayAspectRatio.Num(), info.DisplayAspectRatio.Denom())
		fmt.Printf("\tfield_order: %s\n", info.FieldOrder)
	}

	fmt.Printf("Container\n")
	fmt.Printf("\tformat_name: %s\n", probe.ContainerInfo.FormatName)
	fmt.Printf("\tduration: %.5f\n", probe.ContainerInfo.Duration)

	return nil
}
