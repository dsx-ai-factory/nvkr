# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Invoked only by the fixed launcher in an authenticated, read-only appliance.
# sysread deliberately leaves the subject suffix on stdin for the executed tool.
use strict;
use warnings;
use Config;
use Digest::SHA;
use Fcntl qw(SEEK_SET);

sub refuse {
    my ($code, $message) = @_;
    print STDERR "nvkr sealed bootstrap: $message\n";
    exit $code;
}

sub digest_handle {
    my ($handle, $limit) = @_;
    my $hash = Digest::SHA->new(256);
    my $size = 0;
    while (1) {
        my $count = sysread($handle, my $buffer, 65536);
        defined($count) or refuse(29, "cannot measure executable");
        last if $count == 0;
        $size += $count;
        $size <= $limit or refuse(22, "executable exceeds bound");
        $hash->add($buffer);
    }
    return ("sha256:" . $hash->hexdigest, $size);
}

my ($mode, $platform, $interpreter, $expected, $size) = splice(@ARGV, 0, 5);
defined($size) && ($mode eq "probe" || $mode eq "execute")
    or refuse(29, "unsupported bootstrap request");
$interpreter =~ /\Asha256:[0-9a-f]{64}\z/
    or refuse(22, "invalid interpreter binding");
$Config{ptrsize} == 8 && $Config{ivsize} == 8
    or refuse(29, "unsupported pointer ABI");
my ($memfd_call, $exec_call);
if ($platform eq "linux/arm64" && $Config{archname} =~ /\Aaarch64-linux/) {
    ($memfd_call, $exec_call) = (279, 281);
} elsif ($platform eq "linux/amd64" && $Config{archname} =~ /\Ax86_64-linux/) {
    ($memfd_call, $exec_call) = (319, 322);
} else {
    refuse(29, "interpreter architecture differs from selection");
}
open(my $self, "<", "/proc/self/exe") or refuse(29, "interpreter is unavailable");
my ($actual) = digest_handle($self, 64 * 1024 * 1024);
close($self) or refuse(29, "cannot close interpreter measurement");
$actual eq $interpreter or refuse(22, "interpreter bytes differ");

my $input;
if ($mode eq "probe") {
    @ARGV == 0 && $expected eq "-" && $size eq "0"
        or refuse(29, "invalid capability probe");
    open($input, "<", "/bin/true") or refuse(29, "probe executable is unavailable");
    ($expected, $size) = digest_handle($input, 1024 * 1024);
    sysseek($input, 0, SEEK_SET) == 0 or refuse(29, "cannot rewind probe");
} else {
    $expected =~ /\Asha256:[0-9a-f]{64}\z/ && $size =~ /\A[1-9][0-9]{0,8}\z/
        && $size <= 512 * 1024 * 1024 && @ARGV > 0
        or refuse(22, "invalid helper binding");
    $input = \*STDIN;
}

my $name = "nvkr-inspection-helper";
my $fd = syscall($memfd_call, $name, 3); # CLOEXEC | ALLOW_SEALING
$fd >= 0 or refuse(29, "anonymous executable unavailable");
open(my $mem, "+<&=$fd") or refuse(29, "cannot own anonymous executable");
my $remaining = $size;
while ($remaining > 0) {
    my $count = sysread($input, my $buffer, $remaining < 65536 ? $remaining : 65536);
    defined($count) && $count > 0 or refuse(22, "truncated helper stream");
    my $offset = 0;
    while ($offset < $count) {
        my $written = syswrite($mem, $buffer, $count - $offset, $offset);
        defined($written) && $written > 0 or refuse(29, "cannot populate anonymous executable");
        $offset += $written;
    }
    $remaining -= $count;
}
chmod(0555, $mem) == 1 or refuse(29, "cannot make anonymous executable runnable");
fcntl($mem, 1033, 15) or refuse(29, "cannot seal anonymous executable");
fcntl($mem, 1034, 0) == 15 or refuse(29, "anonymous executable lacks required seals");
!defined(syswrite($mem, "x")) or refuse(29, "sealed executable accepted a write");
sysseek($mem, 0, SEEK_SET) == 0 or refuse(29, "cannot measure sealed executable");
my ($digest, $measured_size) = digest_handle($mem, $size);
$digest eq $expected && $measured_size == $size
    or refuse(22, "sealed helper bytes differ");

# Probe the same execveat path before producer work. The parent reports success
# only after the child has actually executed the image-owned probe and exited.
if ($mode eq "probe") {
    my $pid = fork();
    defined($pid) or refuse(29, "cannot fork capability probe");
    if ($pid != 0) {
        waitpid($pid, 0) == $pid && $? == 0 or refuse(29, "descriptor execution probe failed");
        print STDOUT "nvkr-sealed-exec-v1\n";
        exit 0;
    }
}
my @argv = ("nvkr-disk-inspect", @ARGV);
my @environment = ("PATH=/usr/bin:/bin", "HOME=/tmp", "LC_ALL=C", "TZ=UTC");
for (@argv, @environment) {
    index($_, "\0") == -1 or refuse(22, "invalid executable argument");
    $_ .= "\0";
}
my $arguments = pack("J*", (map { unpack("J", pack("P", $_)) } @argv), 0);
my $environment = pack("J*", (map { unpack("J", pack("P", $_)) } @environment), 0);
my $empty = "";
syscall($exec_call, $fd, $empty, $arguments, $environment, 4096); # AT_EMPTY_PATH
refuse(29, "descriptor execution unavailable");
