if [ -z "$FFMPEG_DIST" ]
then
	echo "FFMPEG_DIST environment variable must be set"
	exit 1
fi

if [ $# -eq 0 ]
then
	go test -timeout 10000s
else
	echo "Running the $1 test(s)"
	go test -run $1 -timeout 10000s
fi
