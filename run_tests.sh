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

go test --timeout 10000s

