# OwnGit installer for Windows (Windows PowerShell 5.1 and PowerShell 7).
#
#   irm https://owngit.app/install.ps1 | iex
#   & ([scriptblock]::Create((irm https://owngit.app/install.ps1))) -Version 1.1.3 -NoService
#
# It downloads the release archive over HTTPS, checks it against the
# SHA256SUMS file of the same release, unpacks it into a folder named after
# the release under -Dir (default %LOCALAPPDATA%\Programs\OwnGit) and runs
# "owngit service install" from there. Nothing on this computer changes
# before the archive matches SHA256SUMS. A running program cannot be
# replaced on Windows, so every release gets its own folder; an earlier
# one stays until you delete it. OWNGIT_RELEASES replaces the release
# address (https only), for a mirror. Paths are used literally, so [ and ]
# in a folder name are ordinary characters.
param(
    [string]$Version = '',
    [switch]$NoService,
    [string]$Dir = ''
)

function Install-OwnGit([string]$Version, [bool]$NoService, [string]$Dir) {
    $ErrorActionPreference = 'Stop'
    $Version = $Version -replace '^v', ''
    if ($Version -and $Version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+$') {
        throw "-Version takes a release number such as 1.1.3, not $Version."
    }
    $releases = $env:OWNGIT_RELEASES
    if (-not $releases) { $releases = 'https://github.com/juliankang4/owngit/releases' }
    if (-not $releases.StartsWith('https://')) {
        throw "OWNGIT_RELEASES must be an https:// address, not $releases."
    }
    $arch = $env:PROCESSOR_ARCHITEW6432
    if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
    if ($arch -ne 'AMD64') {
        throw "OwnGit for Windows is released for x64 computers; this one is $arch. Build OwnGit from source."
    }

    if (-not $Dir) { $Dir = [IO.Path]::Combine($env:LOCALAPPDATA, 'Programs', 'OwnGit') }
    $Dir = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Dir).TrimEnd('\', '/')
    # "owngit service install" keeps its protected copy in
    # %ProgramFiles%\OwnGit, and "owngit uninstall" removes what it finds
    # there, so the program you install lives elsewhere.
    $serviceFolder = [IO.Path]::Combine($env:ProgramFiles, 'OwnGit')
    if ($Dir -eq $serviceFolder -or $Dir.StartsWith($serviceFolder + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw "$serviceFolder belongs to ""owngit service install""; choose another -Dir."
    }

    $web = New-Object Net.WebClient
    function Fetch([string]$Url, [string]$File) {
        try {
            if ($File) { $web.DownloadFile($Url, $File) } else { $web.DownloadString($Url) }
        } catch {
            throw "Could not download ${Url}: $($_.Exception.GetBaseException().Message) Nothing was changed."
        }
    }
    function DigestOf([string]$File) {
        (Get-FileHash -LiteralPath $File -Algorithm SHA256).Hash.ToLowerInvariant()
    }

    # The digest and the archive name come from the release's SHA256SUMS.
    # Without -Version, the name of the latest release's archive gives its
    # version, so both downloads are from the same release.
    if ($Version) {
        $sums = Fetch "$releases/download/v$Version/SHA256SUMS"
        $pattern = [regex]::Escape("owngit_${Version}_windows_amd64.zip")
    } else {
        $sums = Fetch "$releases/latest/download/SHA256SUMS"
        $pattern = 'owngit_[0-9]+\.[0-9]+\.[0-9]+_windows_amd64\.zip'
    }
    $lines = @($sums -split "`n" | ForEach-Object { $_.TrimEnd("`r") } | Where-Object { $_ -cmatch "^[0-9a-f]{64}  $pattern$" })
    if ($lines.Count -ne 1) {
        throw "The release's SHA256SUMS does not list one archive for windows/amd64. Nothing was changed."
    }
    $digest, $name = $lines[0] -split '  ', 2
    $Version = $name.Split('_')[1]
    $folder = [IO.Path]::Combine($Dir, "owngit_${Version}_windows_amd64")
    $program = [IO.Path]::Combine($folder, 'owngit.exe')

    $tmp = [IO.Path]::Combine([IO.Path]::GetTempPath(), 'owngit-install-' + [Guid]::NewGuid().ToString('N'))
    [void][IO.Directory]::CreateDirectory($tmp)
    $zip = [IO.Path]::Combine($tmp, $name)
    # Unpacked beside the final folder, so moving it there is one rename on
    # the same drive and the folder never holds half a release.
    $staged = [IO.Path]::Combine($Dir, '.owngit-' + [Guid]::NewGuid().ToString('N'))
    try {
        Fetch "$releases/download/v$Version/$name" $zip
        $actual = DigestOf $zip
        if ($actual -ne $digest) {
            throw "$name does not match the release's SHA256SUMS (got $actual, expected $digest). Nothing was changed."
        }
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        if ([IO.Directory]::Exists($folder)) {
            $archive = [IO.Compression.ZipFile]::OpenRead($zip)
            try {
                $entry = $archive.GetEntry('owngit.exe')
                $same = $false
                if ($entry -and [IO.File]::Exists($program)) {
                    $check = [Security.Cryptography.SHA256]::Create()
                    $stream = $entry.Open()
                    try { $same = ([BitConverter]::ToString($check.ComputeHash($stream)) -replace '-', '').ToLowerInvariant() -eq (DigestOf $program) }
                    finally { $stream.Dispose() }
                }
            } finally { $archive.Dispose() }
            if (-not $same) {
                throw "$folder exists but does not hold this release. Move it away and run the installer again."
            }
            "OwnGit $Version is already in $folder."
        } else {
            [void][IO.Directory]::CreateDirectory($Dir)
            [IO.Compression.ZipFile]::ExtractToDirectory($zip, $staged)
            if (-not [IO.File]::Exists([IO.Path]::Combine($staged, 'owngit.exe'))) {
                throw "$name holds no owngit.exe. Nothing was changed."
            }
            [IO.Directory]::Move($staged, $folder)
            "Installed OwnGit $Version in $folder."
        }
    } finally {
        if ([IO.Directory]::Exists($staged)) { [IO.Directory]::Delete($staged, $true) }
        if ([IO.File]::Exists($zip)) { [IO.File]::Delete($zip) }
        [IO.Directory]::Delete($tmp)
    }

    # PowerShell ends a single-quoted string at any of these quote marks, so
    # each is doubled.
    $run = "& '" + ($program -replace "(['\u2018-\u201B])", '$1$1') + "'"
    if ($NoService) {
        "Run it now with: $run serve"
        "Or run it as a service that starts by itself: $run service install"
        return
    }
    # Messages that owngit writes to standard error are not failures; its
    # exit code is.
    $ErrorActionPreference = 'Continue'
    & $program service install
    if ($LASTEXITCODE -ne 0) {
        throw """owngit service install"" did not finish. OwnGit $Version stays in $folder; after fixing what it reported, run: $run service install"
    }
}

Install-OwnGit -Version $Version -NoService $NoService.IsPresent -Dir $Dir
