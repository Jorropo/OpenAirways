const std = @import("std");
const Child = std.process.Child;
const Step = std.Build.Step;

pub fn build(b: *std.Build) !void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{});

    //
    //
    // Dependencies
    //
    //

    const raylib_dep = b.dependency("raylib_zig", .{
        .target = target,
        .optimize = optimize,
    });
    const raylib = raylib_dep.module("raylib"); // main raylib module
    const raygui = raylib_dep.module("raygui"); // raygui module
    const raylib_artifact = raylib_dep.artifact("raylib"); // raylib C library

    const exe = b.addExecutable(.{
        .name = "OpenAirways",
        .root_module = b.createModule(.{
            .root_source_file = b.path("game/main.zig"),
            .target = target,
            .optimize = optimize,
        }),
    });

    exe.linkLibrary(raylib_artifact);
    exe.root_module.addImport("raylib", raylib);
    exe.root_module.addImport("raygui", raygui);

    //
    //
    // Build Go Server
    //
    //

    const server_build_mode = b.option(BuildServerMode, "server-build", "Server build mode") orelse BuildServerMode.default;

    const main_pkg = b.path("cmd/server").getPath(b);
    const out = b.path("game-server").getPath(b);

    const build_server = std.Build.Step.Run.create(b, "build server");
    build_server.addArgs(&.{ "go", "build", "-o", out, main_pkg });

    if (server_build_mode == .default) {
        b.getInstallStep().dependOn(&build_server.step);
    }

    b.installArtifact(exe);

    const run_cmd = b.addRunArtifact(exe);

    run_cmd.step.dependOn(b.getInstallStep());

    if (b.args) |args| {
        run_cmd.addArgs(args);
    }

    const run_step = b.step("run", "Run the app");
    run_step.dependOn(&run_cmd.step);

    const exe_unit_tests = b.addTest(.{
        .name = "game-tests",
        .root_module = b.createModule(.{
            .root_source_file = b.path("game/main.zig"),
            .target = target,
            .optimize = optimize,
        }),
    });

    const run_exe_unit_tests = b.addRunArtifact(exe_unit_tests);

    // Similar to creating the run step earlier, this exposes a `test` step to
    // the `zig build --help` menu, providing a way for the user to request
    // running the unit tests.
    const test_step = b.step("test", "Run unit tests");
    test_step.dependOn(&run_exe_unit_tests.step);
}

const BuildServerMode = enum { default, skip };
