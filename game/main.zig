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

    var cl: Client = .{
        .plane_size = .{ .x = @floatFromInt(plane_img.width), .y = @floatFromInt(plane_img.height) },
    };

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

                try plane.draw(allocator, state, plane_img, cl.plane_size, highlight, true);
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
    plane_size: V2,

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
            .none => try InputState.handle_none(cl, game),
        }
    }

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

    fn handle_none(cl: *Client, game: *Game) !void {
        const mouse_pos = rl.getMousePosition();

        if (rl.isMouseButtonPressed(rl.MouseButton.mouse_button_left)) {
            const clicked = Plane.intersecting_plane(&game.state, cl.plane_size, cl.camera, mouse_pos);
            if (clicked) |p| {
                cl.input = .{ .plane_target = .{
                    .id = p.id,
                    .current = mouse_pos,
                } };
            }
        }
    }
};

const InputPlaneTarget = struct {
    id: u32,
    current: V2,

    fn handle(cl: *Client, game: *Game) !void {
        const target = cl.input.plane_target;
        const mouse_pos = rl.getMousePosition();

        cl.input.plane_target.current = mouse_pos;

        if (rl.isMouseButtonReleased(rl.MouseButton.mouse_button_left)) {
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
        }
    }
};
