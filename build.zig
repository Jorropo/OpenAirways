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

    const raylib_dep = b.dependency("raylib-zig", .{
        .target = target,
        .optimize = optimize,
    });
    const raylib = raylib_dep.module("raylib"); // main raylib module
    const raygui = raylib_dep.module("raygui"); // raygui module
    const raylib_artifact = raylib_dep.artifact("raylib"); // raylib C library

    const exe = b.addExecutable(.{
        .name = "hh-scope",
        .root_source_file = b.path("game/main.zig"),
        .target = target,
        .optimize = optimize,
    });

    exe.linkLibrary(raylib_artifact);
    exe.root_module.addImport("raylib", raylib);
    exe.root_module.addImport("raygui", raygui);

    //
    //
    // Build Go Server
    //
    //

    const buildServerStep = BuildServerStep.create(b);
    b.getInstallStep().dependOn(&buildServerStep.step);

    b.installArtifact(exe);

    const run_cmd = b.addRunArtifact(exe);

    run_cmd.step.dependOn(b.getInstallStep());

    if (b.args) |args| {
        run_cmd.addArgs(args);
    }

    const run_step = b.step("run", "Run the app");
    run_step.dependOn(&run_cmd.step);

    const exe_unit_tests = b.addTest(.{
        .root_source_file = b.path("game/main.zig"),
        .target = target,
        .optimize = optimize,
    });

    const run_exe_unit_tests = b.addRunArtifact(exe_unit_tests);

    // Similar to creating the run step earlier, this exposes a `test` step to
    // the `zig build --help` menu, providing a way for the user to request
    // running the unit tests.
    const test_step = b.step("test", "Run unit tests");
    test_step.dependOn(&run_exe_unit_tests.step);
}

const BuildServerMode = enum { default, skip };

const BuildServerStep = struct {
    step: Step,
    mode: BuildServerMode,

    pub fn create(b: *std.Build) *BuildServerStep {
        const server_build_mode = b.option(BuildServerMode, "server-build", "Server build mode") orelse BuildServerMode.default;

        const ptr = b.allocator.create(BuildServerStep) catch @panic("OOM");
        ptr.* = .{
            .step = Step.init(.{
                .id = .custom,
                .name = "build server",
                .owner = b,
                .makeFn = make,
            }),
            .mode = server_build_mode,
        };
        return ptr;
    }

    pub fn make(step: *Step, _: std.Progress.Node) anyerror!void {
        const self: *BuildServerStep = @fieldParentPtr("step", step);
        const b = step.owner;

        switch (self.mode) {
            .default => {
                const main_pkg = b.path("cmd/server").getPath(b);
                const out = b.path("game-server").getPath(b);
                var go_build = [_][]const u8{ "go", "build", "-o", out, main_pkg };
                var child = Child.init(&go_build, b.allocator);
                var current = try std.process.getEnvMap(b.allocator);
                defer current.deinit();
                _ = try current.put("GOEXPERIMENT", "rangefunc"); // FIXME: remove once updating to go1.23
                child.env_map = &current;
                _ = try child.spawnAndWait();
            },
            .skip => {},
        }
    }
};
