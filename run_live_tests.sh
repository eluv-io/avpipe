if ! [ -x `command -v gsutil` ]
then
    echo "gsutil could not be found, install gsutil"
    exit 1
fi

if [ ! -d "./media" ]
then
    mkdir ./media
    gsutil -m cp 'gs://eluvio-test-assets/*' ./media
fi

echo "Running live UDP tests"
go test -run TestUdpToMp4 ./live/

echo "Running live probe tests"
go test -run TestProbe ./live/

#echo "Running live HLS tests"
#go test -run TestHLS ./live/
