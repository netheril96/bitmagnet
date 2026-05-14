#!/usr/bin/env python3
import json
import httpx
import argparse

GRAPHQL_QUERY = """
query TorrentContentSearch($input: TorrentContentSearchQueryInput!) {
  torrentContent {
    search(input: $input) {
      items {
        id
        infoHash
        contentType
        title
        seeders
        leechers
        publishedAt
        torrent {
          name
          size
          filesCount
          fileType
          magnetUri
        }
        content {
          type
          title
          releaseYear
        }
      }
      hasNextPage
    }
  }
}
"""

def main():
    parser = argparse.ArgumentParser(description="Stream bitmagnet search results to a file.")
    parser.add_argument("--query", required=True, help="Search query string")
    parser.add_argument("--endpoint", default="http://localhost:3333/graphql", help="Bitmagnet GraphQL endpoint (URL or path to unix socket)")
    parser.add_argument("--output", default="results.jsonl", help="Output file (JSON Lines format)")
    parser.add_argument("--limit", type=int, default=100, help="Results per page")
    args = parser.parse_args()

    transport = None
    url = args.endpoint

    # Check if endpoint is a unix socket
    if args.endpoint.startswith("/") or args.endpoint.startswith("./"):
        transport = httpx.HTTPTransport(uds=args.endpoint)
        url = "http://localhost/graphql"
        print(f"Connecting to Unix socket: {args.endpoint}")
    else:
        print(f"Connecting to HTTP endpoint: {args.endpoint}")

    page = 1
    has_next = True
    count = 0

    with httpx.Client(transport=transport, timeout=None) as client:
        with open(args.output, "w") as f:
            while has_next:
                payload = {
                    "query": GRAPHQL_QUERY,
                    "variables": {
                        "input": {
                            "queryString": args.query,
                            "limit": args.limit,
                            "page": page,
                            "hasNextPage": True,
                        }
                    }
                }

                response = client.post(url, json=payload)
                response.raise_for_status()
                
                data = response.json()
                if "errors" in data:
                    print(f"GraphQL Errors: {json.dumps(data['errors'], indent=2)}")
                    break

                search_result = data["data"]["torrentContent"]["search"]
                items = search_result["items"]
                has_next = search_result["hasNextPage"]
                
                for item in items:
                    f.write(json.dumps(item) + "\n")
                    f.flush()
                    count += 1

                print(f"Page {page}: Received {len(items)} items (Total: {count})")
                
                if not items:
                    break
                    
                page += 1

    print(f"Done! Streamed {count} items to {args.output}")

if __name__ == "__main__":
    main()
