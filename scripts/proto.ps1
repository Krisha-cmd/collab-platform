<#
.SYNOPSIS
    Lints the .proto files and generates Go, Python and TypeScript code from them.

.DESCRIPTION
    Replaces "make proto". Works in Windows PowerShell 5.1 and PowerShell 7,
    and can be run from any folder: it always works from the repo root.

.EXAMPLE
    .\scripts\proto.ps1            # format check + lint + generate (the default)
.EXAMPLE
    .\scripts\proto.ps1 lint       # lint only
.EXAMPLE
    .\scripts\proto.ps1 format     # rewrite .proto files in buf's standard style
.EXAMPLE
    .\scripts\proto.ps1 breaking   # check for breaking changes against git main
#>
param(
    [ValidateSet("all", "lint", "gen", "format", "breaking")]
    [string]$Task = "all"
)

$ErrorActionPreference = "Stop"

# The repo root is the folder above scripts\
$RepoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $RepoRoot

function Assert-Buf {
    if (-not (Get-Command buf -ErrorAction SilentlyContinue)) {
        Write-Host "buf is not installed or not on PATH." -ForegroundColor Red
        Write-Host "Install it with:  winget install bufbuild.buf"
        Write-Host "then close and reopen your terminal."
        exit 1
    }
}

# Runs one buf command and stops the script if it fails.
# (Native programs report failure through $LASTEXITCODE, not exceptions.)
function Invoke-Buf {
    param([string]$Step, [string[]]$BufArgs)
    Write-Host "==> $Step" -ForegroundColor Cyan
    & buf @BufArgs
    if ($LASTEXITCODE -ne 0) {
        Write-Host "FAILED: $Step (buf exited with code $LASTEXITCODE)" -ForegroundColor Red
        exit $LASTEXITCODE
    }
}

Assert-Buf

switch ($Task) {
    "lint" {
        Invoke-Buf "Lint" @("lint")
    }
    "gen" {
        Invoke-Buf "Generate code" @("generate")
    }
    "format" {
        Invoke-Buf "Format .proto files" @("format", "-w")
    }
    "breaking" {
        Invoke-Buf "Check for breaking changes vs main" @("breaking", "--against", ".git#branch=main")
    }
    "all" {
        Invoke-Buf "Check formatting" @("format", "--diff", "--exit-code")
        Invoke-Buf "Lint" @("lint")
        Invoke-Buf "Generate code" @("generate")
    }
}

Write-Host "Done." -ForegroundColor Green
