package chatgpt

import "testing"

func TestExtractImageToolMsgsDetectsIMG2SedimentOnly(t *testing.T) {
	mapping := map[string]interface{}{
		"tool-node": map[string]interface{}{
			"message": map[string]interface{}{
				"author":      map[string]interface{}{"role": "tool"},
				"create_time": float64(1760000000),
				"metadata": map[string]interface{}{
					"async_task_type": "image_gen",
					"model_slug":      "gpt-5-3",
				},
				"content": map[string]interface{}{
					"content_type": "multimodal_text",
					"parts": []interface{}{
						map[string]interface{}{
							"content_type":  "image_asset_pointer",
							"asset_pointer": "sediment://file_000000001d4071fd83437b6e5d5bcaa9",
							"width":         float64(1536),
							"height":        float64(1024),
							"size_bytes":    float64(2540679),
							"metadata": map[string]interface{}{
								"generation": map[string]interface{}{
									"gen_size":    "image",
									"gen_size_v2": "48",
									"orientation": "landscape",
								},
							},
						},
					},
				},
			},
		},
	}

	msgs := ExtractImageToolMsgs(mapping)
	if len(msgs) != 1 {
		t.Fatalf("len(msgs)=%d, want 1", len(msgs))
	}
	msg := msgs[0]
	if !msg.IMG2Hint {
		t.Fatalf("IMG2Hint=false, want true")
	}
	if got, want := msg.SedimentIDs, []string{"file_000000001d4071fd83437b6e5d5bcaa9"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("SedimentIDs=%v, want %v", got, want)
	}
	if got, want := msg.GenSizeV2s, []string{"48"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("GenSizeV2s=%v, want %v", got, want)
	}
	if msg.MaxWidth != 1536 || msg.MaxHeight != 1024 || msg.MaxSizeBytes != 2540679 {
		t.Fatalf("metadata dims/size = %dx%d %d", msg.MaxWidth, msg.MaxHeight, msg.MaxSizeBytes)
	}
}

func TestParseImageSSEDetectsIMG2Sediment(t *testing.T) {
	stream := make(chan SSEEvent, 1)
	stream <- SSEEvent{Data: []byte(`{"v":{"message":{"content":{"parts":[{"asset_pointer":"sediment://file_img2","metadata":{"generation":{"gen_size_v2":"48"}}}]}}}}`)}
	close(stream)

	res := ParseImageSSE(stream)
	if got, want := res.SedimentIDs, []string{"file_img2"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("SedimentIDs=%v, want %v", got, want)
	}
	if got, want := res.IMG2SedimentIDs, []string{"file_img2"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("IMG2SedimentIDs=%v, want %v", got, want)
	}
}
