module github.com/MrWong99/Glyphoxa

go 1.26

require (
	github.com/antzucaro/matchr v0.0.0-20221106193745-7bed6ef61ef9
	github.com/disgoorg/disgo v0.19.4 // pinned: pkg/voice depends on the voice.Conn/OpusFrame* API; bump deliberately
	github.com/disgoorg/godave/golibdave v0.1.0 // DAVE/MLS glue, used only under -tags dave (CGO + libdave)
	github.com/disgoorg/snowflake/v2 v2.0.3
	github.com/yalue/onnxruntime_go v1.30.1
	gopkg.in/Regis24GmbH/go-phonetics.v3 v3.0.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/disgoorg/godave v0.1.0 // indirect
	github.com/disgoorg/godave/libdave v0.1.0 // indirect
	github.com/disgoorg/json/v2 v2.0.0 // indirect
	github.com/disgoorg/omit v1.0.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/klauspost/compress v1.18.4 // indirect
	github.com/sasha-s/go-csync v0.0.0-20240107134140-fcbab37b09ad // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	gopkg.in/Regis24GmbH/go-diacritics.v2 v2.0.3 // indirect
)
