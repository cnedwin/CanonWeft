package retrieval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"time"
)

const ModelRevision = "5617a9f61b028005a4858fdac845db406aefb181"
const Dimension = 1024

var ErrEmbedding = errors.New("本地语义模型未就绪或返回数据未通过校验")

type Embedder struct {
	endpoint, token string
	client          *http.Client
}

func NewEmbedder(endpoint, token string) (*Embedder, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || len(token) < 32 {
		return nil, ErrEmbedding
	}
	return &Embedder{endpoint: "http://" + u.Host, token: token, client: &http.Client{
		Timeout:       90 * time.Second,
		Transport:     &http.Transport{Proxy: nil, MaxConnsPerHost: 2, ResponseHeaderTimeout: 85 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrEmbedding },
	}}, nil
}

func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if e == nil || len(texts) < 1 || len(texts) > 8 {
		return nil, ErrEmbedding
	}
	payload, err := json.Marshal(map[string]any{"texts": texts})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint+"/embed", bytes.NewReader(payload))
	if err != nil {
		return nil, ErrEmbedding
	}
	request.Header.Set("Authorization", "Bearer "+e.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := e.client.Do(request)
	if err != nil {
		return nil, ErrEmbedding
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, ErrEmbedding
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return nil, ErrEmbedding
	}
	var result struct {
		Model     string      `json:"model"`
		Revision  string      `json:"revision"`
		Dimension int         `json:"dimension"`
		Vectors   [][]float32 `json:"vectors"`
		Hashes    []string    `json:"text_hashes"`
		Tokens    []int       `json:"token_counts"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Model != "BAAI/bge-m3" || result.Revision != ModelRevision || result.Dimension != Dimension || len(result.Vectors) != len(texts) || len(result.Hashes) != len(texts) || len(result.Tokens) != len(texts) {
		return nil, ErrEmbedding
	}
	for index, vector := range result.Vectors {
		sum := sha256.Sum256([]byte(texts[index]))
		if result.Hashes[index] != hex.EncodeToString(sum[:]) || result.Tokens[index] < 1 || result.Tokens[index] > 2048 || !ValidVector(vector) {
			return nil, ErrEmbedding
		}
	}
	return result.Vectors, nil
}

func ValidVector(vector []float32) bool {
	if len(vector) != Dimension {
		return false
	}
	var norm float64
	for _, value := range vector {
		v := float64(value)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
		norm += v * v
	}
	return math.Abs(norm-1) < 0.002
}

// All stored vectors are validated and normalized; dot product is cosine similarity.
func Cosine(left, right []float32) float64 {
	if len(left) != len(right) {
		return -1
	}
	var sum float64
	for index, value := range left {
		sum += float64(value) * float64(right[index])
	}
	return sum
}
