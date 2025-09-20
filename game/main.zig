const rl = @import("raylib");
const Game = @import("Game.zig");

const std = @import("std");
const print = std.debug.print;

const Child = std.process.Child;

const Plane = Game.Plane;
const V2 = Game.V2;
const Rect = Game.Rect;
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
    const const_server_args: [][:0]const u8 = @ptrCast(server_args);
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

    const plane_img = try rl.loadTexture("assets/plane_1.png");
    defer plane_img.unload();

    var cl: Client = .{};

    game.read_state.init.wait();
    server_args[0] = programName; // restore for argsFree
    std.process.argsFree(allocator, server_args);

    // setup camera
    cl.lerp_camera(state.camera_size, 1);

    while (!rl.windowShouldClose()) { // Detect window close button or ESC key
        rl.beginDrawing();
        const frameClock: i64 = @intCast(game.timer.read());

        try cl.handle_input(&game);
        cl.lerp_camera(state.camera_size, 0.1);

        defer rl.endDrawing();
        defer rl.drawFPS(8, 8); // always draw last

        rl.clearBackground(rl.getColor(0x11111100));

        game.mu.lock();

        const mouse_pos = rl.getMousePosition();

        // don't draw with the camera. runways should have a static size.
        for (state.runways) |r| {
            const highlight = cl.input == .plane_to_runway and cl.input.plane_to_runway.runway.id == r.id;
            try r.draw(cl.camera, highlight);
        }

        {
            cl.camera.begin();
            defer cl.camera.end();

            const nanosPerTick = std.time.ns_per_s / state.tick_rate;
            const deltans = frameClock - @as(i64, state.now - game.timer_base) * nanosPerTick;
            state.delta_ticks = @as(f32, @floatFromInt(deltans)) / @as(f32, @floatFromInt(nanosPerTick));

            for (state.planes) |plane| {
                // const highlight = cl.input == .plane_target and cl.input.plane_target.id == plane.id;
                const highlight = switch (cl.input) {
                    .plane_target => |t| t.id == plane.id,
                    .plane_to_runway => |t| t.plane_id == plane.id,
                    else => false,
                };

                try plane.draw(allocator, state, plane_img, highlight, false);
                if (highlight) {
                    const loc = plane.current_pos(state);
                    const target = switch (cl.input) {
                        .plane_to_runway => |t| t.runway.closest_end(loc, cl.camera),
                        else => rl.getScreenToWorld2D(mouse_pos, cl.camera),
                    };

                    rl.drawLineEx(loc, target, 4, rl.Color.red);
                }
            }
        }

        game.mu.unlock();
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

    fn handle_input(cl: *Client, game: *Game) !void {
        switch (cl.input) {
            .plane_target => try InputPlaneTarget.handle(cl, game),
            .plane_to_runway => try InputPlaneToRunway.handle(cl, game),
            .none => try InputState.handle_none(cl, game),
        }
    }

    fn lerp_camera(cl: *Client, camera_size: Rect, n: f32) void {
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
    plane_to_runway: InputPlaneToRunway,
    none,

    fn handle_none(cl: *Client, game: *Game) !void {
        const mouse_pos = rl.getMousePosition();

        if (rl.isMouseButtonPressed(rl.MouseButton.left)) {
            const clicked = Plane.intersecting_plane(&game.state, mouse_pos, cl.camera);
            if (clicked) |p| {
                cl.input = .{ .plane_target = .{
                    .id = p.id,
                } };
            }
        }
    }
};

const InputPlaneTarget = struct {
    id: u32,

    fn handle(cl: *Client, game: *Game) !void {
        const target = cl.input.plane_target;
        const mouse_pos = rl.getMousePosition();
        const mouse_delta = rl.getMouseDelta();

        if (mouse_delta.lengthSqr() != 0) {
            for (game.state.runways) |r| {
                if (r.intersecting_runway(mouse_pos, cl.camera)) {
                    cl.input = .{ .plane_to_runway = .{
                        .plane_id = target.id,
                        .runway = r,
                    } };
                    return;
                }
            }
        }

        if (rl.isMouseButtonReleased(rl.MouseButton.left)) {
            var plane_pos: ?V2 = null;
            for (game.state.planes) |p| {
                if (p.id == target.id) {
                    plane_pos = rl.getWorldToScreen2D(p.current_pos(&game.state), cl.camera);
                    break;
                }
            }

            // the plane no longer exists. reached waypoint while held?
            if (plane_pos == null) {
                cl.input = .none;
                return;
            }

            cl.input = .none;
            try game.give_plane_heading(
                target.id,
                plane_pos.?,
                mouse_pos,
            );
            return;
        }
    }
};

const InputPlaneToRunway = struct {
    plane_id: u32,
    runway: Game.Runway,

    fn handle(cl: *Client, game: *Game) !void {
        const target = cl.input.plane_to_runway;
        const mouse_pos = rl.getMousePosition();
        const mouse_delta = rl.getMouseDelta();

        if (mouse_delta.lengthSqr() != 0) {
            // this should always work. runways is never modified
            // after game init.
            var r: Game.Runway = undefined;
            for (game.state.runways) |runway| {
                if (target.runway.id == runway.id) {
                    r = runway;
                    break;
                }
            }

            if (!r.intersecting_runway(mouse_pos, cl.camera)) {
                cl.input = .{ .plane_target = .{
                    .id = target.plane_id,
                } };
                return;
            }
        }

        if (rl.isMouseButtonReleased(rl.MouseButton.left)) {
            print("send aircraft {} to runway {}\n", .{ target.plane_id, target.runway.id });
            cl.input = .none;
            return;
        }
    }
};
