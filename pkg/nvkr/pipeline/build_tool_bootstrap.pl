# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# The pinned source-build image owns this interpreter. The exact native tool
# arrives on stdin, never through a host executable mount or PATH selection.
use strict;
use warnings;
use Config;
use Digest::SHA;
use Fcntl qw(SEEK_SET F_SETFD);
use POSIX ();

my ($platform, $expected, $size) = splice(@ARGV, 0, 3);
defined($size) && $size =~ /\A[1-9][0-9]{0,8}\z/ && $size <= 512 * 1024 * 1024
    && $expected =~ /\Asha256:[0-9a-f]{64}\z/ && @ARGV > 0
    or die "invalid build tool binding\n";
$Config{ptrsize} == 8 && $Config{ivsize} == 8 or die "unsupported pointer ABI\n";
my $memfd_call;
if ($platform eq "linux/arm64" && $Config{archname} =~ /\Aaarch64-linux/) {
    $memfd_call = 279;
} elsif ($platform eq "linux/amd64" && $Config{archname} =~ /\Ax86_64-linux/) {
    $memfd_call = 319;
} else {
    die "build tool architecture differs\n";
}
my $name = "nvkr-build-tool";
my $fd = syscall($memfd_call, $name, 3);
$fd >= 0 or die "anonymous executable unavailable\n";
POSIX::dup2($fd, 200) == 200 or die "cannot reserve executable descriptor\n";
POSIX::close($fd) if $fd != 200;
open(my $mem, "+<&=200") or die "cannot own executable descriptor\n";
my $remaining = $size;
while ($remaining > 0) {
    my $count = sysread(STDIN, my $buffer, $remaining < 65536 ? $remaining : 65536);
    defined($count) && $count > 0 or die "truncated build tool stream\n";
    my $offset = 0;
    while ($offset < $count) {
        my $written = syswrite($mem, $buffer, $count - $offset, $offset);
        defined($written) && $written > 0 or die "cannot populate executable\n";
        $offset += $written;
    }
    $remaining -= $count;
}
my $suffix_size = sysread(STDIN, my $suffix, 1);
defined($suffix_size) && $suffix_size == 0 or die "excess or unreadable build tool bytes\n";
chmod(0555, $mem) == 1 or die "cannot make tool executable\n";
fcntl($mem, 1033, 15) or die "cannot seal executable\n";
fcntl($mem, 1034, 0) == 15 or die "executable lacks required seals\n";
!defined(syswrite($mem, "x")) or die "sealed executable accepted a write\n";
my $offset = sysseek($mem, 0, SEEK_SET);
defined($offset) && $offset == 0 or die "cannot measure executable\n";
my $hash = Digest::SHA->new(256);
$hash->addfile($mem);
"sha256:" . $hash->hexdigest eq $expected or die "build tool digest mismatch\n";
fcntl($mem, F_SETFD, 0) or die "cannot inherit executable\n";
close(STDIN) or die "cannot close tool stream\n";
exec {$ARGV[0]} @ARGV;
die "cannot execute source build: $!\n";
