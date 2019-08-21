

# How to run the live HLS reader

``` bash

cd .../avpipe/live

go test --run TestToolTs

ls -l lhr_out.ts
```

# How to run avpipe live fmp4 mez

(we need to specify seg-duration because it is a required parameter - but it is ignored)

``` bash

cd .../avpipe

./bin/etx -format fmp4 -f live/lhr_out.ts -seg-duration-ts 1 -seg-duration-fr 1

ls -l O/O1/fmp4-stream.mp4
```
