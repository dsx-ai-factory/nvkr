exec python3 /src/os-image/packer/scripts/compose-image-inputs.py \
  --architecture amd64 \
  --materials-dir /nvkr/inputs/materials \
  --source-files-dir /src/os-image/files \
  --output /src/nvkr-output/image-inputs.tar \
  --source-date-epoch "$SOURCE_DATE_EPOCH"
