package cmd

import (
	"fmt"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
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
	cmdProbe.PersistentFlags().BoolP("listen", "", false, "listen mode for RTMP.")
	cmdProbe.PersistentFlags().Int32("connection-timeout", 0, "connection timeout for RTMP when listening on a port or MPEGTS to receive first UDP datagram.")

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

	connectionTimeout, err := cmd.Flags().GetInt32("connection-timeout")
	if err != nil {
		return fmt.Errorf("Invalid connection-timeout flag")
	}

	listen, err := cmd.Flags().GetBool("listen")
	if err != nil {
		return fmt.Errorf("Invalid listen flag")
	}

	params := &goavpipe.XcParams{
		Url:               filename,
		Seekable:          seekable,
		Listen:            listen,
		ConnectionTimeout: int(connectionTimeout),
	}

	goavpipe.InitIOHandler(&elvxcInputOpener{url: filename}, &elvxcOutputOpener{dir: ""})

	probe, err := avpipe.Probe(params)
	if err != nil {
		return fmt.Errorf("Probing failed. file=%s", filename)
	}

	for _, info := range probe.StreamInfo {
		channelLayoutName := "-"
		if info.CodecType == "audio" {
			channelLayoutName = avpipe.ChannelLayoutName(info.Channels, info.ChannelLayout)
		}

		fmt.Printf("Stream[%d]\n", info.StreamIndex)
		fmt.Printf("\tstream_id: %d\n", info.StreamId)
		fmt.Printf("\tcodec_type: %s\n", info.CodecType)
		fmt.Printf("\tcodec_id: %d\n", info.CodecID)
		fmt.Printf("\tcodec_name: %s\n", info.CodecName)
		fmt.Printf("\tprofile: %s\n", avpipe.GetProfileName(info.CodecID, info.Profile))
		fmt.Printf("\tlevel: %d\n", info.Level)
		if uint64(info.DurationTs) != goavpipe.AvNoPtsValue {
			fmt.Printf("\tduration_ts: %d\n", info.DurationTs)
		}
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
		/* TODO: Make this a switch based on different SideData */
		if info.SideData != nil && len(info.SideData) > 0 {
			displayMatrix, ok := info.SideData[0].(avpipe.SideDataDisplayMatrix)
			if ok {
				fmt.Printf("\tside_data:\n")
				fmt.Printf("\t\tdisplay_matrix:\n")
				fmt.Printf("\t\t\trotation: %f\n", displayMatrix.Rotation)
				fmt.Printf("\t\t\trotation_cw: %f\n", displayMatrix.RotationCw)
			}
		}
		if info.Tags != nil {
			fmt.Printf("\ttags:\n")
			for k, v := range info.Tags {
				fmt.Printf("\t\t%s: %s\n", k, v)
			}
		}
	}

	fmt.Printf("Container\n")
	fmt.Printf("\tformat_name: %s\n", probe.ContainerInfo.FormatName)
	fmt.Printf("\tduration: %.5f\n", probe.ContainerInfo.Duration)

	return nil
}
