# Platform Tools Specification

Every platform connector in Claude Bridge must implement this standard set of MCP tools. This ensures Claude has a consistent interface regardless of which platform it's talking to.

## Tool Naming Convention

Tools follow the pattern: `{action}_{platform}_{resource}`

Examples: `get_facebook_status`, `create_facebook_post`, `list_facebook_contacts`

## Required Tools Per Platform

### 1. Status & Profile

| Tool | Description | Returns |
|------|-------------|---------|
| `get_{platform}_status` | Connection status | `connected`, `user_name`, `profile_pic`, `page_name` (if applicable) |

### 2. Messaging

| Tool | Description | Parameters | Returns |
|------|-------------|------------|---------|
| `list_{platform}_contacts` | List conversations/contacts | `limit`, `refresh` | Array of contacts with `id`, `name`, `profile_pic`, `last_message_at` |
| `read_{platform}_messages` | Read messages in a conversation | `conversation_id` (required), `limit`, `refresh` | Array of messages with `id`, `sender`, `content`, `timestamp`, `is_outgoing` |
| `send_{platform}_message` | Send a message | `recipient_id` (required), `message` (required) | Success/failure |

### 3. Posts & Content

| Tool | Description | Parameters | Returns |
|------|-------------|------------|---------|
| `create_{platform}_post` | Create a new post | `content` (required), `page_url` (optional), `media_url` (optional) | `post_id`, success/failure |
| `get_{platform}_posts` | Get recent posts | `limit`, `page_url` (optional), `refresh` | Array of posts with `id`, `content`, `posted_at`, `likes`, `comments_count`, `shares` |

### 4. Comments & Engagement

| Tool | Description | Parameters | Returns |
|------|-------------|------------|---------|
| `get_{platform}_comments` | Get comments on a post | `post_id` (required), `limit` | Array of comments with `id`, `author`, `content`, `timestamp`, `likes` |
| `reply_{platform}_comment` | Reply to a comment | `comment_id` (required), `message` (required) | Success/failure |

### 5. Search

| Tool | Description | Parameters | Returns |
|------|-------------|------------|---------|
| `search_{platform}` | Search contacts, messages, or posts | `query` (required), `type` (`contacts`/`messages`/`posts`), `limit` | Mixed results depending on type |

### 6. Analytics

| Tool | Description | Returns |
|------|-------------|---------|
| `get_{platform}_analytics` | Engagement analytics | `total_posts`, `total_likes`, `total_comments`, `total_shares`, `avg_engagement` |

### 7. Batch Operations

| Tool | Description | Parameters | Returns |
|------|-------------|------------|---------|
| `batch_{platform}_posts` | Queue multiple posts with pacing | `posts[]` (required), `min_delay_seconds`, `max_delay_seconds` | `batch_id` |
| `batch_{platform}_messages` | Queue multiple messages with pacing | `messages[]` (required), `min_delay_seconds`, `max_delay_seconds` | `batch_id` |
| `get_batch_status` | Check batch progress | `batch_id` (required) | `status`, `progress`, `total`, `jobs[]` |
| `cancel_batch` | Cancel a running batch | `batch_id` (required) | Success/failure |

## Batch Pacing Rules

Batch operations use human-like delays to avoid detection:
- Default: 5-10 seconds between each action
- Random jitter added (±1s) for more natural timing
- Claude submits the full list in one call, the app handles pacing
- Progress is tracked per-job so Claude can report status
- Batches can be cancelled mid-run (completed jobs stay completed)

## Caching Rules

All read tools (list, read, get) follow the cache-first pattern:

1. Default: return cached data with a `synced_at` timestamp
2. If `refresh=true`: fetch fresh data from the platform, update cache, return fresh data
3. Claude sees the `synced_at` timestamp and can suggest refreshing if data is stale
4. Response format always includes: `ok`, `cached`, `synced_at`, `data`

## Platform Implementation Status

| Tool | WhatsApp | Facebook | Instagram | LinkedIn | Xiao Hong Shu |
|------|----------|----------|-----------|----------|---------------|
| status | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| list_contacts | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| read_messages | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| send_message | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| create_post | ❌ | ✅ | ⬜ | ⬜ | ⬜ |
| get_posts | ❌ | 🔧 | ⬜ | ⬜ | ⬜ |
| get_comments | ❌ | 🔧 | ⬜ | ⬜ | ⬜ |
| reply_comment | ❌ | 🔧 | ⬜ | ⬜ | ⬜ |
| search | ❌ | 🔧 | ⬜ | ⬜ | ⬜ |

Legend: ✅ Done | 🔧 In Progress | ⬜ Not Started | ❌ N/A for platform

## Notes

- WhatsApp doesn't have posts/comments — those tools are N/A
- Facebook uses hybrid: browser automation for posting, Graph API for messaging
- All platforms store profile pic URL and display name on login
- Future: vector database for semantic search across all platforms
