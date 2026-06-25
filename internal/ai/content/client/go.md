# pgmi API Client — Go

**Recommended generator:** `oapi-codegen`

```bash
oapi-codegen -generate types,client -package api http://localhost:8080/openapi.json > api/client.gen.go
```

## Transport Core Skeleton

```go
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

type ProblemDetails struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func (c *Client) do(req *http.Request, result any) error {
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 400 {
		var problem ProblemDetails
		if json.Unmarshal(body, &problem) == nil && problem.Title != "" {
			return fmt.Errorf("%d %s: %s", problem.Status, problem.Title, problem.Detail)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	if result != nil {
		return json.Unmarshal(body, result)
	}
	return nil
}
```

## Anti-Copy Directive

**DO NOT copy this skeleton verbatim.** It demonstrates auth + error handling only. Generate your endpoint methods from the live `/openapi.json` spec using `oapi-codegen`, then call them through the `do` method above. Do not hand-write endpoint URLs or request shapes from memory.
