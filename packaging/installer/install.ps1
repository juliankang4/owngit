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
    # Not trimmed: D:\ stays a root, where D: would mean the current folder
    # of that drive.
    $Dir = [IO.Path]::GetFullPath($ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Dir))
    # "owngit service install" keeps its protected copy in the OwnGit folder
    # of the 64-bit Program Files, and "owngit uninstall" removes what it
    # finds there, so the program you install lives elsewhere. A 32-bit
    # PowerShell's ProgramFiles names Program Files (x86), so ProgramW6432
    # comes first. Both sides are full paths, compared without regard to
    # case.
    $programFiles = $env:ProgramW6432
    if (-not $programFiles) { $programFiles = $env:ProgramFiles }
    if ($programFiles) {
        $serviceFolder = [IO.Path]::GetFullPath([IO.Path]::Combine($programFiles, 'OwnGit')).TrimEnd('\')
        if (($Dir.TrimEnd('\', '/') + '\').StartsWith($serviceFolder + '\', [StringComparison]::OrdinalIgnoreCase)) {
            throw "$serviceFolder belongs to ""owngit service install""; choose another -Dir."
        }
    }

    # Fetch follows at most five redirects itself, and only to HTTPS, so no
    # hop of a download can be plain HTTP.
    function Fetch([string]$Url, [string]$File) {
        $uri = [Uri]$Url
        for ($hop = 0; ; $hop++) {
            if ($uri.Scheme -ne 'https') { throw "Could not download $Url (it leads to $uri, which is not HTTPS). Nothing was changed." }
            if ($hop -gt 5) { throw "Could not download $Url (too many redirects). Nothing was changed." }
            $request = [Net.HttpWebRequest]::Create($uri)
            $request.AllowAutoRedirect = $false
            try { $response = $request.GetResponse() }
            catch {
                $reason = $_.Exception.GetBaseException().Message.TrimEnd('.')
                for ($e = $_.Exception; $e; $e = $e.InnerException) {
                    if ($e -is [Net.WebException] -and $e.Response) { $reason = "HTTP $([int]$e.Response.StatusCode)"; break }
                }
                throw "Could not download $Url ($reason). Nothing was changed."
            }
            try {
                $code = [int]$response.StatusCode
                if ($code -ge 300 -and $code -lt 400) {
                    $location = $response.Headers['Location']
                    if (-not $location) { throw "Could not download $Url (HTTP $code without a location). Nothing was changed." }
                    $uri = [Uri]::new($uri, $location)
                    continue
                }
                $stream = $response.GetResponseStream()
                if (-not $File) { return (New-Object IO.StreamReader($stream)).ReadToEnd() }
                $out = [IO.File]::Create($File)
                try { $stream.CopyTo($out) } finally { $out.Dispose() }
                return
            } catch [IO.IOException], [Net.WebException] {
                throw "Could not download $Url ($($_.Exception.GetBaseException().Message.TrimEnd('.'))). Nothing was changed."
            } finally { $response.Dispose() }
        }
    }
    function HashOf([IO.Stream]$Stream) {
        $sha = [Security.Cryptography.SHA256]::Create()
        try { ([BitConverter]::ToString($sha.ComputeHash($Stream)) -replace '-', '').ToLowerInvariant() } finally { $sha.Dispose() }
    }
    function DigestOf([string]$File) {
        $stream = [IO.File]::OpenRead($File)
        try { HashOf $stream } finally { $stream.Dispose() }
    }

    # Nobody but this account, SYSTEM, the administrators and TrustedInstaller
    # (and CREATOR OWNER, which means whoever creates an entry) may be able
    # to change the folders the installer uses; otherwise another account
    # could swap the checked program before it runs.
    $trusted = @([Security.Principal.WindowsIdentity]::GetCurrent().User.Value, 'S-1-5-18', 'S-1-5-32-544',
        'S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464', 'S-1-3-0')
    # Why another account can change the folder, or nothing. For the folder
    # the installer creates entries in ($Holds), any right to create or
    # remove entries counts, inheritable ones included, since what the
    # installer creates inherits them; for the folders above it, the rights
    # to rename or re-permit the way.
    function Test-Folder([string]$Folder, [bool]$Holds) {
        if ([IO.File]::GetAttributes($Folder) -band [IO.FileAttributes]::ReparsePoint) { return 'it is a link' }
        try { $acl = Get-Acl -LiteralPath $Folder } catch { return 'its permissions cannot be read' }
        if ($trusted -notcontains $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value) { return 'it belongs to another account' }
        $mask = 0x100D0040
        if ($Holds) { $mask = 0x500D0046 }
        foreach ($rule in $acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])) {
            if ($rule.AccessControlType -ne 'Allow' -or $trusted -contains $rule.IdentityReference.Value) { continue }
            if (-not $Holds -and ($rule.PropagationFlags -band [Security.AccessControl.PropagationFlags]::InheritOnly)) { continue }
            if (([int64][int]$rule.FileSystemRights) -band $mask) {
                $who = $rule.IdentityReference.Value
                try { $who = $rule.IdentityReference.Translate([Security.Principal.NTAccount]).Value } catch { }
                return "$who can change it"
            }
        }
    }
    # Require-Way checks every folder from the drive root to $Folder, which
    # exists; $Folder is the one the installer creates entries in.
    function Require-Way([string]$Folder, [string]$Use) {
        $root = [IO.Path]::GetPathRoot($Folder)
        if ($root.StartsWith('\\') -or [IO.DriveInfo]::new($root).DriveType -eq 'Network') {
            throw "$Folder is on a network share, whose server decides who can change it; $Use."
        }
        $way = $root
        $parts = @($Folder.Substring($root.Length).Split([char[]]'\/', [StringSplitOptions]::RemoveEmptyEntries))
        for ($i = -1; $i -lt $parts.Count; $i++) {
            if ($i -ge 0) { $way = [IO.Path]::Combine($way, $parts[$i]) }
            $reason = Test-Folder $way ($i -eq $parts.Count - 1)
            if ($reason) { throw "Another account can change $way ($reason); $Use." }
        }
    }
    $existing = $Dir
    while (-not [IO.Directory]::Exists($existing)) {
        $existing = [IO.Path]::GetDirectoryName($existing)
        if (-not $existing) { throw "$Dir is on a drive that does not exist." }
    }
    Require-Way $existing 'choose a folder that only you can change with -Dir'
    $tempParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    Require-Way $tempParent.TrimEnd('\') 'set TEMP to a folder that only you can change'

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
        # The digest of owngit.exe in the checked archive, which the program
        # that runs below must have.
        $archive = [IO.Compression.ZipFile]::OpenRead($zip)
        try {
            $entry = $archive.GetEntry('owngit.exe')
            if (-not $entry) { throw "$name holds no owngit.exe. Nothing was changed." }
            $stream = $entry.Open()
            try { $expected = HashOf $stream } finally { $stream.Dispose() }
        } finally { $archive.Dispose() }
        if ([IO.Directory]::Exists($folder)) {
            if (-not [IO.File]::Exists($program) -or (DigestOf $program) -ne $expected) {
                throw "$folder exists but does not hold this release. Move it away and run the installer again."
            }
            "OwnGit $Version is already in $folder."
        } else {
            [void][IO.Directory]::CreateDirectory($Dir)
            [IO.Compression.ZipFile]::ExtractToDirectory($zip, $staged)
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
    # The program stays open, readable by others but not replaceable, from
    # its last check until "owngit service install" has run from it.
    $held = [IO.File]::Open($program, 'Open', 'Read', 'Read')
    try {
        if ((HashOf $held) -ne $expected) { throw "$program changed after it was checked; run the installer again." }
        if ($NoService) {
            "Run it now with: $run serve"
            "Or run it as a service that starts by itself: $run service install"
            return
        }
        # Messages that owngit writes to standard error are not failures;
        # its exit code is.
        $ErrorActionPreference = 'Continue'
        & $program service install
        if ($LASTEXITCODE -ne 0) {
            throw """owngit service install"" did not finish. OwnGit $Version stays in $folder; after fixing what it reported, run: $run service install"
        }
    } finally { $held.Dispose() }
}

try {
    Install-OwnGit -Version $Version -NoService $NoService.IsPresent -Dir $Dir
} catch {
    # One plain line, without PowerShell's error record around it, so a
    # command in the message can be copied as it is.
    $Host.UI.WriteErrorLine('owngit install: ' + $_.Exception.Message)
    # Then a short terminating error, so the command fails in every form:
    # powershell -File and -Command exit with 1, and a script that runs the
    # installer stops there. PowerShell prints its own few lines for it.
    # exit is never used, because under iex it would close the window.
    throw 'OwnGit was not installed.'
}
