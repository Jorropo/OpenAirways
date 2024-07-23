const rl = @import("raylib");
const Game = @import("Game.zig");

const std = @import("std");
const print = std.debug.print;

const Child = std.process.Child;

const Plane = Game.Plane;
const V2 = Game.V2;
const screen = Game.screen;

pub fn main() anyerror!void {
    rl.setConfigFlags(rl.ConfigFlags{
        .msaa_4x_hint = true,
        .vsync_hint = true,
    });
    rl.initWindow(screen.x, screen.y, "OpenAirways");
    defer rl.closeWindow();

    var gpa = std.heap.GeneralPurposeAllocator(.{}){};
    const allocator = gpa.allocator();
    defer {
        const check = gpa.deinit();
        switch (check) {
            .leak => std.debug.print("Allocator deinit error\n", .{}),
            .ok => {},
        }
    }

    var server_args = try std.process.argsAlloc(allocator);
    const programName = server_args[0];
    const const_server_args: [][:0]const u8 = server_args;
    const_server_args[0] = "./game-server";

    var proc = Child.init(const_server_args, allocator);
    proc.stdin_behavior = .Pipe;
    proc.stdout_behavior = .Pipe;
    proc.stderr_behavior = .Inherit;

    var game = Game{
        .allocator = allocator,
        .server_proc = proc,
    };
    var state = &game.state;

    try game.start_server();

    const plane_img = rl.loadTexture("assets/plane_1.png");
    defer plane_img.unload();

    const plane_w: f32 = @floatFromInt(plane_img.width);
    const plane_h: f32 = @floatFromInt(plane_img.height);
    const plane_size = V2.init(plane_w, plane_h);

    var cl: Client = .{};

    game.read_state.init.wait();
    server_args[0] = programName; // restore for argsFree
    std.process.argsFree(allocator, server_args);

    // setup camera
    cl.lerp_camera(game.state.camera_size, 1);

    while (!rl.windowShouldClose()) { // Detect window close button or ESC key
        rl.beginDrawing();
        const frameClock: i64 = @intCast(game.timer.read());

        // input handling
        const mouse_pos = rl.getMousePosition();
        switch (cl.input) {
            .plane_target => |target| blk: {
                cl.input.plane_target.current = mouse_pos;

                if (rl.isMouseButtonReleased(rl.MouseButton.mouse_button_left)) {
                    var plane_pos: ?V2 = null;
                    for (state.planes) |p| {
                        if (p.id == target.id) {
                            plane_pos = rl.getWorldToScreen2D(p.current_pos(state), cl.camera);
                            break;
                        }
                    }

                    // the plane no longer exists. reached waypoint while held?
                    if (plane_pos == null) {
                        print("plane held ({}) no longer able to be found. planes: {any}\n", .{ target.id, state.planes });
                        cl.input = .none;
                        break :blk;
                    }

                    cl.input = .none;
                    try game.give_plane_heading(
                        target.id,
                        plane_pos.?,
                        mouse_pos,
                    );
                    break :blk;
                }
            },

            .none => {
                if (rl.isMouseButtonPressed(rl.MouseButton.mouse_button_left)) {
                    const clicked = Plane.intersecting_plane(state, plane_size, cl.camera, mouse_pos);
                    if (clicked) |p| {
                        cl.input = .{ .plane_target = .{
                            .id = p.id,
                            .current = mouse_pos,
                        } };
                    }
                }
            },
        }

        cl.lerp_camera(game.state.camera_size, 0.1);

        defer rl.endDrawing();
        defer rl.drawFPS(8, 8); // always draw last

        rl.clearBackground(rl.Color.white);

        {
            game.mu.lock();
            defer game.mu.unlock();
            cl.camera.begin();
            defer cl.camera.end();

            const nanosPerTick = std.time.ns_per_s / state.tick_rate;
            const deltans = frameClock - @as(i64, state.now - game.timer_base) * nanosPerTick;
            state.delta_ticks = @as(f32, @floatFromInt(deltans)) / @as(f32, @floatFromInt(nanosPerTick));

            for (state.planes) |plane| {
                const highlight = cl.input == .plane_target and cl.input.plane_target.id == plane.id;

                try plane.draw(allocator, state, plane_img, plane_size, highlight, true);
                if (highlight) {
                    const loc = plane.current_pos(state);
                    const current_in_world = rl.getScreenToWorld2D(cl.input.plane_target.current, cl.camera);
                    rl.drawLineEx(loc, current_in_world, 4, rl.Color.red);
                }
            }
        }
    }

    _ = try game.server_proc.kill();
    game.server_thread.join();
}

const Client = struct {
    input: InputState = .none,
    camera: rl.Camera2D = .{ // centered around 0,0
        .offset = .{ .x = screen.x / 2, .y = screen.y / 2 },
        .target = .{ .x = 0, .y = 0 },
        .rotation = 0,
        .zoom = 1,
    },

    fn lerp_camera(cl: *Client, camera_size: rl.Rectangle, n: f32) void {
        const target = V2{
            .x = camera_size.x + camera_size.width / 2,
            .y = camera_size.y + camera_size.height / 2,
        };
        const zoom = screen.x / camera_size.width;

        cl.camera.target = V2{
            .x = std.math.lerp(cl.camera.target.x, target.x, n),
            .y = std.math.lerp(cl.camera.target.y, target.y, n),
        };
        cl.camera.zoom = std.math.lerp(cl.camera.zoom, zoom, n);
    }
};

const InputState = union(enum) {
    plane_target: InputPlaneTarget,
    none,
};

const InputPlaneTarget = struct {
    id: u32,
    current: V2,
};
