#!/usr/bin/env pwsh
# Test script: Verify opencode2api responses for OpenAI API compatibility

$baseUrl = "http://localhost:3000"
$headers = @{ "x-api-key" = "oc_sk_103d308fe5d0_vc-IekjPQ_ZLnPH7XHJC8jTTdyY4QaDP" }

$tests = @(
    @{
        Name = "Non-tool-calling (nemotron-3.5-lightning-free)"
        Model = "nemotron-3.5-lightning-free"
        Prompt = "Say 'pong' in one word"
        Stream = $false
    }
    @{
        Name = "Tool-calling (muse-spark-1.3-contributor-free)"
        Model = "muse-spark-1.3-contributor-free"
        Prompt = "Show me the files in /data"
        Stream = $false
    }
    @{
        Name = "Non-tool-calling streaming"
        Model = "nemotron-3.5-lightning-free"
        Prompt = "Count from 1 to 5 one number per line"
        Stream = $true
    }
    @{
        Name = "Tool-calling streaming"
        Model = "muse-spark-1.3-contributor-free"
        Prompt = "List files in /tmp"
        Stream = $true
    }
    @{
        Name = "Reasoning model (non-tool-calling)"
        Model = "mimo-v2.6-flash-free"
        Prompt = "What is the capital of France? Answer only the city name."
        Stream = $false
    }
)

$passCount = 0
$failCount = 0

foreach ($t in $tests) {
    Write-Output ""
    Write-Output "============================================"
    Write-Output "TEST: $($t.Name)"
    Write-Output "  Model:    $($t.Model)"
    Write-Output "  Prompt:   $($t.Prompt)"
    Write-Output "  Stream:   $($t.Stream)"
    Write-Output "--------------------------------------------"

    if ($t.Stream) {
        # Stream test - use WebRequest to capture SSE
        $body = @{
            model = $t.Model
            messages = @( @{ role = "user"; content = $t.Prompt } )
            stream = $true
        } | ConvertTo-Json -Compress

        try {
            $resp = Invoke-WebRequest -Uri "$baseUrl/v1/chat/completions" -Method POST -Headers $headers -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) -ContentType "application/json" -TimeoutSec 60 -UseBasicParsing
            Write-Output "  HTTP Status: $($resp.StatusCode)"
            Write-Output "  Content-Type: $($resp.Headers['Content-Type'])"
            
            # Parse SSE
            $sseLines = $resp.Content -split "`n" | Where-Object { $_.Trim() }
            $chunks = @()
            $firstChunkId = $null
            $lastChunkFinish = $null
            $lastChunkDeltaContent = ""
            $lastChunkDeltaToolCalls = $null
            $hasDoneMarker = $false
            $errorInStream = $null

            foreach ($line in $sseLines) {
                if ($line -match '^data: (.+)$') {
                    $data = $Matches[1]
                    if ($data -eq "[DONE]") {
                        $hasDoneMarker = $true
                        continue
                    }
                    try {
                        $chunk = $data | ConvertFrom-Json -ErrorAction Stop
                        # Track ID consistency
                        if (-not $firstChunkId) { $firstChunkId = $chunk.id }
                        if ($chunk.id -ne $firstChunkId) {
                            $errorInStream = "Inconsistent chunk IDs: $firstChunkId vs $($chunk.id)"
                        }
                        if ($chunk.choices[0].delta) {
                            if ($chunk.choices[0].delta.content) {
                                $lastChunkDeltaContent += $chunk.choices[0].delta.content
                            }
                            if ($chunk.choices[0].delta.tool_calls) {
                                $lastChunkDeltaToolCalls = $chunk.choices[0].delta.tool_calls
                            }
                        }
                        if ($chunk.choices[0].finish_reason) {
                            $lastChunkFinish = $chunk.choices[0].finish_reason
                        }
                    } catch {
                        $errorInStream = "Failed to parse chunk: $_"
                    }
                }
            }

            Write-Output "  SSE Chunks: $($sseLines.Count) lines"
            Write-Output "  Done marker: $($hasDoneMarker.ToString().ToLower())"
            Write-Output "  Chunk count: $($chunkIds.Count)"
            Write-Output "  ID consistency: $(if ($firstChunkId) { 'OK' } else { 'NO ID FOUND' })"

            # Check content/finish/tool_calls structure
            if ($errorInStream) {
                Write-Output "  ERROR: $errorInStream"
                $failCount++
                continue
            }

            Write-Output "  Final finish_reason: '$($lastChunkFinish)'"
            Write-Output "  Final content: '$($lastChunkDeltaContent)'"

            if ($lastChunkDeltaToolCalls) {
                $toolNames = @()
                foreach ($tc in $lastChunkDeltaToolCalls) {
                    $toolNames += $tc.function.name
                }
                Write-Output "  Tool calls detected: $($toolNames -join ', ')"
                # For tool calling models, we expect finish_reason to be tool_calls (or stop if the client handles it differently)
                if ($lastChunkFinish -ne "tool_calls" -and $lastChunkFinish -ne "stop") {
                    Write-Output "  WARNING: finish_reason is '$lastChunkFinish' but tool calls detected"
                }
                if ($lastChunkDeltaContent -eq "") {
                    Write-Output "  NOTE: No content text (pure tool call response is valid)"
                } else {
                    Write-Output "  NOTE: Content present: '$($lastChunkDeltaContent)'"
                }
                Write-Output "  PASS: Tool call streaming works correctly"
                $passCount++
            } else {
                if ($lastChunkFinish -eq "stop" -or $lastChunkFinish -eq "tool_calls") {
                    Write-Output "  PASS: Non-tool-calling response valid (finish=$lastChunkFinish, content='$($lastChunkDeltaContent.Substring(0, [Math]::Min(50, $lastChunkDeltaContent.Length)))...')"
                    $passCount++
                } else {
                    Write-Output "  FAIL: finish_reason='$lastChunkFinish' but no tool_calls"
                    $failCount++
                }
            }
        } catch {
            Write-Output "  FAIL: $($_.Exception.Message)"
            if ($_.ErrorDetails) { Write-Output "  Response: $($_.ErrorDetails.Message)" }
            $failCount++
        }
    } else {
        # Non-streaming test - capture exact raw JSON
        $body = @{
            model = $t.Model
            messages = @( @{ role = "user"; content = $t.Prompt } )
            stream = $false
        } | ConvertTo-Json -Compress

        try {
            $raw = Invoke-WebRequest -Uri "$baseUrl/v1/chat/completions" -Method POST -Headers $headers -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) -ContentType "application/json" -TimeoutSec 60 -UseBasicParsing -ErrorAction Stop

            Write-Output "  HTTP Status: $($raw.StatusCode)"
            Write-Output "  Content-Type: $($raw.Headers['Content-Type'])"

            # Parse and analyze structure
            $jsonObj = $raw.Content | ConvertFrom-Json

            # Verify required top-level fields
            $hasId = $jsonObj.PSObject.Properties['id'] -and $jsonObj.id
            $hasObject = $jsonObj.PSObject.Properties['object'] -and $jsonObj.object -eq "chat.completion"
            $hasCreated = $jsonObj.PSObject.Properties['created'] -and $jsonObj.created
            $hasModel = $jsonObj.PSObject.Properties['model'] -and $jsonObj.model -eq $t.Model
            $hasChoices = $jsonObj.PSObject.Properties['choices'] -and $jsonObj.choices.Count -gt 0

            Write-Output "  id present: $hasId"
            Write-Output "  object='chat.completion': $hasObject"
            Write-Output "  created present: $hasCreated"
            Write-Output "  model matches: $hasModel"
            Write-Output "  choices present: $hasChoices"

            if (-not ($hasId -and $hasObject -and $hasModel -and $hasChoices)) {
                Write-Output "  FAIL: Missing required top-level fields"
                $failCount++
                continue
            }

            $choice = $jsonObj.choices[0]
            $msg = $choice.message

            # Verify message fields
            $hasRole = $msg.PSObject.Properties['role'] -and $msg.role -eq "assistant"
            $hasContent = $msg.PSObject.Properties['content'] -ne $null
            $contentIsNull = -not $msg.PSObject.Properties['content'] -or $msg.content -eq $null
            $contentIsEmpty = $msg.PSObject.Properties['content'] -and $msg.content -eq ""
            $hasToolCalls = $msg.PSObject.Properties['tool_calls'] -ne $null
            $hasFinishReason = $msg.PSObject.Properties['finish_reason'] -ne $null

            Write-Output "  message.role='assistant': $hasRole"
            Write-Output "  message.content property: present=$hasContent"
            if ($hasContent) {
                Write-Output "  message.content is null: $contentIsNull"
                Write-Output "  message.content is empty string: $contentIsEmpty"
            }
            if ($hasToolCalls) {
                Write-Output "  message.tool_calls count: $($msg.tool_calls.Count)"
                foreach ($tc in $msg.tool_calls) {
                    Write-Output "    - $($tc.function.name) id=$($tc.id)"
                }
            }
            Write-Output "  message.finish_reason='$($msg.finish_reason)'"

            # Validate OpenAI spec compliance
            # When finish_reason == "tool_calls", content should be absent/null and tool_calls must be present
            # When finish_reason == "stop", content should have text and tool_calls should be absent
            $errors = @()
            
            if ($msg.finish_reason -eq "tool_calls") {
                if ($hasToolCalls -and $msg.tool_calls.Count -gt 0) {
                    Write-Output "  OK: finish_reason=tool_calls with tool_calls present"
                } else {
                    $errors += "finish_reason=tool_calls but no tool_calls in response"
                }
                # content should be null or absent (not empty string!)
                if ($contentIsNull -or (-not $hasContent)) {
                    Write-Output "  OK: content is null (correct for tool_calls)"
                } elseif ($contentIsEmpty) {
                    $errors += "content is empty string '' when it should be null/absent for tool_calls"
                    Write-Output "  ERROR: content is empty string (should be null)"
                } else {
                    Write-Output "  OK: content='$($msg.content.Substring(0, [Math]::Min(30, $msg.content.Length)))...'"
                }
            } elseif ($msg.finish_reason -eq "stop") {
                if ($hasToolCalls -and $msg.tool_calls.Count -gt 0) {
                    $errors += "finish_reason=stop but tool_calls present (should be absent)"
                } else {
                    Write-Output "  OK: finish_reason=stop without tool_calls"
                }
                if (-not $hasContent) {
                    $errors += "finish_reason=stop but content property is absent"
                } elseif ($contentIsNull) {
                    $errors += "finish_reason=stop but content is null (should have text)"
                } elseif ($contentIsEmpty) {
                    $errors += "finish_reason=stop but content is empty string ''"
                } else {
                    Write-Output "  OK: content='$($msg.content.Substring(0, [Math]::Min(50, $msg.content.Length)))...'"
                }
            } else {
                $errors += "Unexpected finish_reason: $($msg.finish_reason)"
                Write-Output "  ERROR: Unexpected finish_reason='$($msg.finish_reason)'"
            }

            if ($errors.Count -gt 0) {
                Write-Output "  RAW JSON:"
                Write-Output $raw.Content
                foreach ($err in $errors) {
                    Write-Output "  FAIL: $err"
                }
                $failCount++
            } else {
                Write-Output "  PASS: Valid OpenAI API response"
                $passCount++
            }
        } catch {
            Write-Output "  FAIL: $($_.Exception.Message)"
            if ($_.ErrorDetails) { Write-Output "  Response: $($_.ErrorDetails.Message)" }
            $failCount++
        }
    }
}

Write-Output ""
Write-Output "============================================"
Write-Output "RESULTS: $passCount passed, $failCount failed out of $($tests.Count) tests"
Write-Output "============================================"
